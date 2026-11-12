// stationctl — лёгкий веб-пульт, работающий ВНУТРИ Яндекс.Станции.
//
// Один статический бинарь (stdlib, без зависимостей, CGO off). Поднимает HTTP
// на самой колонке (по умолчанию :8080) и даёт браузерный пульт + JSON API для
// локального управления: громкость (amixer), тест звука (speaker-test), ребут,
// статус (temp/mem/uptime/df/device_id), LED-цвет, хвосты логов /data/quasar,
// и root-шелл. Рассчитан на то, что процесс уже идёт от root (после рутовки).
//
// Сборка под колонку (aarch64, статик):
//   GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" \
//     -o stationctl ./cmd/stationctl
// Или scripts/build-stationctl.sh. Ставится как пакет apkg (kind=file).
//
// ⚠️ Отдаёт root-шелл по сети — только домашняя сеть/VLAN. Токен обязателен
// (генерируется сам, печатается в лог и пишется в $APKG_STATE/stationctl.token).
package main

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const version = "0.3.0"

// --- конфиг (флаги + env) ---

type config struct {
	addr    string
	token   string
	mixer   string // имя ALSA-контрола громкости; "" = автоопределение
	logsDir string
	shell   bool // разрешить /api/exec (root-шелл)

	glagolToken string // прямой conversationToken для glagol :1961
	yandexToken string // аккаунт-токен Яндекса для авто-получения glagol-токена
	ledCmd      string // шаблон команды установки LED (experimental), {arg} = значение

	spotifyBin    string // путь к librespot ("" = искать в PATH)
	spotifyName   string // имя Spotify Connect устройства
	spotifyDevice string // ALSA-устройство вывода для librespot
	spotifyArgs   string // сырые аргументы librespot (перекрывают дефолтные)
}

var cfg config

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func stateDir() string {
	// как у apkg: APKG_STATE, иначе разумный дефолт по flavor
	dirs := []string{"/data/local/var/stationctl", "/data/stationctl", "/tmp/stationctl"}
	if v := os.Getenv("APKG_STATE"); v != "" {
		dirs = []string{v}
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err == nil {
			return d
		}
	}
	return os.TempDir()
}

// --- утилиты запуска команд ---

// maxOutput ограничивает буфер stdout/stderr одной команды (защита от `yes`/`cat /dev/zero`).
const maxOutput = 1 << 20 // 1 МБ

// maxExecSeconds — потолок таймаута /api/exec. WriteTimeout сервера отключён под него.
const maxExecSeconds = 120

// capBuf — буфер с жёстким потолком. Пишется только из copy-горутины os/exec;
// читается строго ПОСЛЕ cmd.Wait() (который дожидается этих горутин) — гонки нет.
type capBuf struct {
	b        strings.Builder
	max      int
	overflow bool
}

func (c *capBuf) Write(p []byte) (int, error) {
	if room := c.max - c.b.Len(); room > 0 {
		if len(p) > room {
			c.b.Write(p[:room])
			c.overflow = true
		} else {
			c.b.Write(p)
		}
	} else {
		c.overflow = true
	}
	return len(p), nil // всегда «приняли», чтобы процесс не падал на EPIPE
}

func (c *capBuf) String() string {
	s := c.b.String()
	if c.overflow {
		s += "\n…(вывод обрезан)"
	}
	return s
}

// augmentedEnv дополняет PATH каталогами станции, чтобы вызванные скрипты
// (например activate_adb.sh → yandex-debugctl) находили бинари даже при скудном
// PATH (запуск из cron/@reboot).
var augEnvOnce struct {
	sync.Once
	env []string
}

func augmentedEnv() []string {
	augEnvOnce.Do(func() {
		path := os.Getenv("PATH")
		parts := strings.Split(path, string(os.PathListSeparator))
		seen := map[string]bool{}
		for _, p := range parts {
			seen[p] = true
		}
		for _, d := range stationDirs {
			if !seen[d] {
				parts = append(parts, d)
			}
		}
		newPath := "PATH=" + strings.Join(parts, string(os.PathListSeparator))
		env := os.Environ()
		replaced := false
		for i, e := range env {
			if strings.HasPrefix(e, "PATH=") {
				env[i] = newPath
				replaced = true
				break
			}
		}
		if !replaced {
			env = append(env, newPath)
		}
		augEnvOnce.env = env
	})
	return augEnvOnce.env
}

func run(timeout time.Duration, name string, args ...string) (string, string, error) {
	outB := &capBuf{max: maxOutput}
	errB := &capBuf{max: maxOutput}
	cmd := exec.Command(name, args...)
	cmd.Stdout = outB
	cmd.Stderr = errB
	cmd.Env = augmentedEnv()
	setPgid(cmd)
	if err := cmd.Start(); err != nil {
		return "", "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var runErr error
	select {
	case runErr = <-done:
	case <-time.After(timeout):
		killTree(cmd.Process)
		<-done // дождаться Wait() (и copy-горутин) перед чтением буферов
		runErr = fmt.Errorf("timeout %s", timeout)
	}
	return outB.String(), errB.String(), runErr
}

var stationDirs = []string{"/sbin", "/usr/sbin", "/usr/bin", "/bin", "/system/bin", "/data/local/bin"}

// lookup ищет бинарь в PATH, затем в типичных каталогах станции (/sbin не всегда в PATH).
func lookup(name string) (string, bool) {
	if p, err := exec.LookPath(name); err == nil {
		return p, true
	}
	for _, d := range stationDirs {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, true
		}
	}
	return "", false
}

// have и resolve используют один и тот же поиск — согласованно.
func have(name string) bool { _, ok := lookup(name); return ok }

func resolve(name string) string {
	if p, ok := lookup(name); ok {
		return p
	}
	return name // пусть exec сам вернёт ошибку
}

func readFirstLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	if s.Scan() {
		return strings.TrimSpace(s.Text())
	}
	return ""
}

// --- сбор статуса станции ---

type status struct {
	Hostname    string            `json:"hostname"`
	DeviceID    string            `json:"device_id"`
	Platform    string            `json:"platform"`
	Uptime      string            `json:"uptime"`
	Root        bool              `json:"root"`
	TempC       float64           `json:"temp_c"`
	Mem         map[string]int64  `json:"mem"`      // total/free/avail в КБ
	Disk        string            `json:"disk"`     // df -h /data (сырьё)
	VolPercent  int               `json:"vol_percent"`
	VolOK       bool              `json:"vol_ok"`
	VolControl  string            `json:"vol_control"`
	Markers     []string          `json:"markers"`  // /data/quasar маркеры рута
	Now         string            `json:"now"`
	Extra       map[string]string `json:"extra,omitempty"`
}

func deviceID() string {
	if have("device_id") {
		// на прошивке quasar: `device_id -r` читает id (без аргумента печатает usage)
		if out, _, err := run(3*time.Second, resolve("device_id"), "-r"); err == nil {
			s := strings.TrimSpace(out)
			if s != "" && !strings.Contains(s, "Usage") {
				return s
			}
		}
	}
	for _, p := range []string{"/data/quasar/device_id", "/factory/device_id"} {
		if s := readFirstLine(p); s != "" {
			return s
		}
	}
	return ""
}

func platform() string {
	// чистый токен платформы (например "plum"), если прошивка его кладёт отдельным файлом
	for _, p := range []string{"/data/quasar/platform", "/tmp/platform"} {
		if s := readFirstLine(p); s != "" && len(s) < 64 && !strings.ContainsAny(s, "{}<>\"") {
			return s
		}
	}
	// модель из device-tree (buildroot) — человекочитаемая строка, обрезаем NUL
	if raw, err := os.ReadFile("/sys/firmware/devicetree/base/model"); err == nil {
		if s := strings.TrimRight(strings.TrimSpace(string(raw)), "\x00"); s != "" {
			return s
		}
	}
	// hostname часто вида YandexLite-gen2-...
	h, _ := os.Hostname()
	return h
}

func cpuTemp() float64 {
	best := 0.0
	for i := 0; i < 16; i++ {
		raw := readFirstLine(fmt.Sprintf("/sys/class/thermal/thermal_zone%d/temp", i))
		if raw == "" {
			continue
		}
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			continue
		}
		if n > 1000 { // милли-градусы
			n /= 1000
		}
		if n > best {
			best = n
		}
	}
	return best
}

func meminfo() map[string]int64 {
	m := map[string]int64{}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return m
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	want := map[string]string{"MemTotal": "total", "MemFree": "free", "MemAvailable": "avail"}
	for s.Scan() {
		parts := strings.Fields(s.Text())
		if len(parts) < 2 {
			continue
		}
		key := strings.TrimSuffix(parts[0], ":")
		if k, ok := want[key]; ok {
			if n, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
				m[k] = n
			}
		}
	}
	return m
}

func uptimeStr() string {
	raw := readFirstLine("/proc/uptime")
	f := strings.Fields(raw)
	if len(f) == 0 {
		return ""
	}
	sec, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return ""
	}
	d := time.Duration(sec) * time.Second
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	m := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dд %dч %dм", days, h, m)
	}
	return fmt.Sprintf("%dч %dм", h, m)
}

func rootMarkers() []string {
	var out []string
	entries, err := os.ReadDir("/data/quasar")
	if err != nil {
		return out
	}
	for _, e := range entries {
		n := e.Name()
		if strings.Contains(n, "adb") || strings.Contains(n, "root") || strings.Contains(n, "quasmodrom") {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func collectStatus() status {
	h, _ := os.Hostname()
	st := status{
		Hostname:   h,
		DeviceID:   deviceID(),
		Platform:   platform(),
		Uptime:     uptimeStr(),
		Root:       os.Geteuid() == 0,
		TempC:      cpuTemp(),
		Mem:        meminfo(),
		Markers:    rootMarkers(),
		Now:        time.Now().Format("2006-01-02 15:04:05"),
		VolControl: cfg.mixer,
	}
	if have("df") {
		if out, _, err := run(3*time.Second, resolve("df"), "-h", "/data"); err == nil {
			st.Disk = strings.TrimSpace(out)
		}
	}
	if p, ctl, ok := getVolume(); ok {
		st.VolPercent = p
		st.VolOK = true
		st.VolControl = ctl
	}
	return st
}

// --- громкость (ALSA/amixer) ---

func mixerControls() []string {
	var ctls []string
	if !have("amixer") {
		return ctls
	}
	out, _, err := run(3*time.Second, resolve("amixer"), "scontrols")
	if err != nil {
		return ctls
	}
	for _, ln := range strings.Split(out, "\n") {
		// Simple mixer control 'Master',0
		if i := strings.Index(ln, "'"); i >= 0 {
			rest := ln[i+1:]
			if j := strings.Index(rest, "'"); j >= 0 {
				ctls = append(ctls, rest[:j])
			}
		}
	}
	return ctls
}

// кэш выбранного контрола: amixer scontrols дорог, а контрол не меняется.
var (
	mixMu     sync.Mutex
	mixCached string
)

// pickMixer — выбор рабочего контрола громкости (с кэшем после первого успеха).
func pickMixer() string {
	if cfg.mixer != "" {
		return cfg.mixer
	}
	mixMu.Lock()
	defer mixMu.Unlock()
	if mixCached != "" {
		return mixCached
	}
	ctls := mixerControls()
	prefer := []string{"Master", "Digital", "PCM", "Playback", "DAC", "Speaker", "HPOUT", "Volume"}
	for _, want := range prefer {
		for _, c := range ctls {
			if strings.EqualFold(c, want) {
				mixCached = c
				return c
			}
		}
	}
	if len(ctls) > 0 {
		mixCached = ctls[0]
		return ctls[0]
	}
	return ""
}

// getVolume -> (percent, control, ok)
func getVolume() (int, string, bool) {
	ctl := pickMixer()
	if ctl == "" || !have("amixer") {
		return 0, "", false
	}
	out, _, err := run(3*time.Second, resolve("amixer"), "sget", ctl)
	if err != nil {
		return 0, ctl, false
	}
	// ищем [NN%]
	for _, ln := range strings.Split(out, "\n") {
		if i := strings.Index(ln, "["); i >= 0 {
			rest := ln[i+1:]
			if j := strings.Index(rest, "%]"); j >= 0 {
				if n, err := strconv.Atoi(strings.TrimSpace(rest[:j])); err == nil {
					return n, ctl, true
				}
			}
		}
	}
	return 0, ctl, false
}

// setVolume -> (percent, ok, error). ok=false, если новую громкость не удалось перечитать.
func setVolume(pct int) (int, bool, error) {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	ctl := pickMixer()
	if ctl == "" || !have("amixer") {
		return 0, false, errors.New("amixer/контрол громкости не найден")
	}
	if _, errOut, err := run(4*time.Second, resolve("amixer"), "-q", "sset", ctl, fmt.Sprintf("%d%%", pct)); err != nil {
		return 0, false, fmt.Errorf("amixer: %v %s", err, strings.TrimSpace(errOut))
	}
	if n, _, ok := getVolume(); ok {
		return n, true, nil
	}
	return pct, false, nil // команда прошла, но перечитать не смогли — возвращаем заданное
}

// --- логи /data/quasar/daemons_logs ---

func logDir() string {
	if cfg.logsDir != "" {
		return cfg.logsDir
	}
	for _, d := range []string{"/data/quasar/daemons_logs", "/data/quasar"} {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d
		}
	}
	return "/data/quasar/daemons_logs"
}

func listLogs() []string {
	var out []string
	entries, err := os.ReadDir(logDir())
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".log") || strings.Contains(e.Name(), ".log") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func tailFile(name string, lines int) (string, error) {
	// защита от выхода за каталог: только имя файла, без разделителей/..
	clean := filepath.Base(name)
	if clean == "." || clean == ".." || clean != name || strings.ContainsRune(name, os.PathSeparator) || strings.ContainsRune(name, '/') {
		return "", errors.New("недопустимое имя файла")
	}
	path := filepath.Join(logDir(), clean)
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", errors.New("это каталог, а не файл")
	}
	return tailLast(f, fi.Size(), lines)
}

// tailPath — хвост произвольного файла (без ограничения каталогом logDir).
func tailPath(path string, lines int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		return "", errors.New("не файл")
	}
	return tailLast(f, fi.Size(), lines)
}

// pidof возвращает pid процесса по имени (через busybox pidof), если есть.
func pidof(name string) (int, bool) {
	if !have("pidof") {
		return 0, false
	}
	out, _, err := run(3*time.Second, resolve("pidof"), name)
	if err != nil {
		return 0, false
	}
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) == 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(f[0])
	if err != nil {
		return 0, false
	}
	return pid, true
}

// tailLast читает последние n строк, двигаясь от конца файла блоками (не читает файл целиком).
func tailLast(f *os.File, size int64, n int) (string, error) {
	if n <= 0 || size == 0 {
		return "", nil
	}
	const block = 64 * 1024
	var (
		buf      []byte
		pos      = size
		newlines int
	)
	for pos > 0 {
		step := int64(block)
		if pos < step {
			step = pos
		}
		pos -= step
		chunk := make([]byte, step)
		if _, err := f.ReadAt(chunk, pos); err != nil {
			return "", err
		}
		buf = append(chunk, buf...)
		// n+1 переводов строки гарантируют n полных последних строк
		newlines = 0
		for _, b := range buf {
			if b == '\n' {
				newlines++
			}
		}
		if newlines > n {
			break
		}
	}
	text := string(buf)
	ls := strings.Split(text, "\n")
	// убрать завершающую пустую строку от финального '\n'
	if len(ls) > 0 && ls[len(ls)-1] == "" {
		ls = ls[:len(ls)-1]
	}
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, "\n"), nil
}

// --- ADB (вкл/выкл через штатный activate_adb.sh) ---

// Расположение маркеров различается по прошивкам: часть кладёт их прямо в /data
// как `quasar_activate_adb_*`, часть — внутрь /data/quasar/ как `activate_adb_*`.
// Проверяем оба варианта.
var adbMarkerPaths = map[string][]string{
	"quasmodrom": {"/data/quasar_activate_adb_quasmodrom", "/data/quasar/activate_adb_quasmodrom"},
	"sound":      {"/data/quasar_activate_adb_sound", "/data/quasar/activate_adb_sound"},
	"glagol":     {"/data/quasar_activate_adb_glagol", "/data/quasar/activate_adb_glagol"},
	"network":    {"/data/quasar_activate_network_adb", "/data/quasar/activate_network_adb"},
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func markerSet(kind string) bool {
	for _, p := range adbMarkerPaths[kind] {
		if fileExists(p) {
			return true
		}
	}
	return false
}

// quasarDir — каталог вендора с activate_adb.sh (${QUASAR}).
func quasarDir() string {
	if v := os.Getenv("QUASAR"); v != "" {
		return v
	}
	for _, d := range []string{"/system/vendor/quasar", "/vendor/quasar", "/data/quasar"} {
		if fileExists(filepath.Join(d, "activate_adb.sh")) {
			return d
		}
	}
	return "/system/vendor/quasar"
}

func activateADBScript() (string, bool) {
	p := filepath.Join(quasarDir(), "activate_adb.sh")
	return p, fileExists(p)
}

// adbListening — слушает ли что-то :5555 (adbd). Порт 5555 = 0x15B3, LISTEN = 0A.
func adbListening() bool {
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, ln := range strings.Split(string(data), "\n") {
			fields := strings.Fields(ln)
			if len(fields) < 4 {
				continue
			}
			if strings.HasSuffix(strings.ToUpper(fields[1]), ":15B3") && fields[3] == "0A" {
				return true
			}
		}
	}
	return false
}

type adbState struct {
	Quasmodrom bool `json:"quasmodrom"`
	Sound      bool `json:"sound"`
	Glagol     bool `json:"glagol"`
	Network    bool `json:"network"`
	Listening  bool `json:"listening"` // реально поднят adbd на :5555
	Enabled    bool `json:"enabled"`   // есть хотя бы один маркер активации
	Script     bool `json:"script"`    // activate_adb.sh на месте
}

func adbStatus() adbState {
	_, ok := activateADBScript()
	s := adbState{
		Quasmodrom: markerSet("quasmodrom"),
		Sound:      markerSet("sound"),
		Glagol:     markerSet("glagol"),
		Network:    markerSet("network"),
		Listening:  adbListening(),
		Script:     ok,
	}
	s.Enabled = s.Quasmodrom || s.Sound || s.Glagol
	return s
}

var adbChannels = map[string]bool{"quasmodrom": true, "sound": true, "glagol": true, "all": true}

// --- HTTP ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func authOK(r *http.Request) bool {
	if cfg.token == "" {
		return true
	}
	// Bearer, X-Token или ?token=
	got := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		got = strings.TrimPrefix(h, "Bearer ")
	}
	if got == "" {
		got = r.Header.Get("X-Token")
	}
	if got == "" {
		got = r.URL.Query().Get("token")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(cfg.token)) == 1
}

// hostAllowed — анти-DNS-rebinding: пускаем только если Host это IP-литерал или localhost.
// Атака через чужой домен (resolve → IP станции) придёт с Host=attacker.tld — отвергаем.
func hostAllowed(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	if h == "" || strings.EqualFold(h, "localhost") {
		return true
	}
	return net.ParseIP(h) != nil
}

// originOK — анти-CSRF: если браузер прислал Origin, он обязан совпасть с Host.
// Любой кросс-сайтовый fetch/форма шлёт Origin; same-origin запрос тоже — и совпадёт.
func originOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true // не браузерный кросс-сайт; защита — host + токен
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// guard: mutating=true требует POST + совпадение Origin (CSRF). Host-проверка — всегда.
func guard(mutating bool, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "недопустимый Host (ожидается IP/localhost)"})
			return
		}
		if mutating {
			if r.Method != http.MethodPost {
				writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "только POST"})
				return
			}
			if !originOK(r) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "кросс-сайтовый Origin запрещён"})
				return
			}
		}
		if !authOK(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "неверный или отсутствующий токен"})
			return
		}
		h(w, r)
	}
}

func apiStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, collectStatus())
}

func apiVolume(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		p, ctl, ok := getVolume()
		writeJSON(w, 200, map[string]any{"percent": p, "control": ctl, "ok": ok})
		return
	}
	// POST (мутация): тот же CSRF-контроль, что и guard(mutating)
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "только GET или POST"})
		return
	}
	if !originOK(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "кросс-сайтовый Origin запрещён"})
		return
	}
	var body struct {
		Percent *int `json:"percent"`
		Delta   *int `json:"delta"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	target := 0
	switch {
	case body.Percent != nil:
		target = *body.Percent
	case body.Delta != nil:
		cur, _, ok := getVolume()
		if !ok {
			writeJSON(w, 500, map[string]string{"error": "не удалось прочитать текущую громкость для delta"})
			return
		}
		target = cur + *body.Delta
	default:
		writeJSON(w, 400, map[string]string{"error": "нужно percent или delta"})
		return
	}
	n, ok, err := setVolume(target)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"percent": n, "ok": ok})
}

func apiMixer(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"controls": mixerControls(), "picked": pickMixer()})
}

func apiBeep(w http.ResponseWriter, r *http.Request) {
	// короткий тон — "найти станцию" / проверка динамика
	if !have("speaker-test") {
		writeJSON(w, 501, map[string]string{"error": "speaker-test не найден"})
		return
	}
	go run(6*time.Second, resolve("speaker-test"), "-t", "sine", "-f", "1000", "-l", "1", "-P", "2")
	writeJSON(w, 200, map[string]string{"ok": "сигнал подан"})
}

func apiLED(w http.ResponseWriter, r *http.Request) {
	if !have("yandex-getcolor") {
		writeJSON(w, 501, map[string]string{"error": "yandex-getcolor не найден"})
		return
	}
	out, errOut, _ := run(3*time.Second, resolve("yandex-getcolor"))
	writeJSON(w, 200, map[string]string{"color": strings.TrimSpace(out), "stderr": strings.TrimSpace(errOut)})
}

func apiReboot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"ok": "перезагружаю станцию"})
	go func() { time.Sleep(500 * time.Millisecond); run(10*time.Second, resolve("reboot")) }()
}

func apiPoweroff(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"ok": "выключаю станцию"})
	go func() {
		time.Sleep(500 * time.Millisecond)
		if have("poweroff") {
			run(10*time.Second, resolve("poweroff"))
		} else {
			run(10*time.Second, resolve("halt"))
		}
	}()
}

func apiADB(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, 200, adbStatus())
		return
	}
	// POST (мутация): CSRF-контроль как у volume
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "только GET или POST"})
		return
	}
	if !originOK(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "кросс-сайтовый Origin запрещён"})
		return
	}
	var body struct {
		Action  string `json:"action"`  // "enable" | "disable"
		Channel string `json:"channel"` // quasmodrom|sound|glagol|all (по умолчанию quasmodrom)
		Network *bool  `json:"network"` // для enable: сетевой adbd (по умолчанию true)
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	script, ok := activateADBScript()
	if !ok {
		writeJSON(w, 501, map[string]string{"error": "activate_adb.sh не найден (не та прошивка?)"})
		return
	}

	var args []string
	switch body.Action {
	case "enable":
		ch := body.Channel
		if ch == "" {
			ch = "quasmodrom"
		}
		if !adbChannels[ch] {
			writeJSON(w, 400, map[string]string{"error": "недопустимый channel"})
			return
		}
		args = []string{"--" + ch, "activate"}
		if body.Network == nil || *body.Network {
			args = append(args, "--network")
		}
	case "disable":
		// снимаем все маркеры и гасим adbd
		args = []string{"--all", "deactivate"}
	default:
		writeJSON(w, 400, map[string]string{"error": "action должен быть enable или disable"})
		return
	}

	out, errOut, err := run(20*time.Second, script, args...)
	resp := map[string]any{"ran": "activate_adb.sh " + strings.Join(args, " "), "state": adbStatus()}
	if out != "" {
		resp["stdout"] = out
	}
	if errOut != "" {
		resp["stderr"] = errOut
	}
	if err != nil {
		resp["error"] = err.Error()
		writeJSON(w, 500, resp)
		return
	}
	writeJSON(w, 200, resp)
}

func apiLogs(w http.ResponseWriter, r *http.Request) {
	file := r.URL.Query().Get("file")
	if file == "" {
		writeJSON(w, 200, map[string]any{"dir": logDir(), "files": listLogs()})
		return
	}
	lines := 200
	if n, err := strconv.Atoi(r.URL.Query().Get("lines")); err == nil && n > 0 && n <= 5000 {
		lines = n
	}
	body, err := tailFile(file, lines)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"file": file, "lines": lines, "text": body})
}

func apiExec(w http.ResponseWriter, r *http.Request) {
	if !cfg.shell {
		writeJSON(w, 403, map[string]string{"error": "шелл выключен (запусти с --shell)"})
		return
	}
	var body struct {
		Cmd     string `json:"cmd"`
		Timeout int    `json:"timeout"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Cmd) == "" {
		writeJSON(w, 400, map[string]string{"error": "нужно поле cmd"})
		return
	}
	to := 20 * time.Second
	if body.Timeout > 0 && body.Timeout <= maxExecSeconds {
		to = time.Duration(body.Timeout) * time.Second
	}
	out, errOut, err := run(to, resolve("sh"), "-c", body.Cmd)
	code := 0
	if err != nil {
		code = -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
	}
	writeJSON(w, 200, map[string]any{"stdout": out, "stderr": errOut, "exit": code})
}

func apiUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(uiHTML))
}

// --- запуск ---

func ensureToken() {
	if cfg.token != "" {
		return
	}
	if v := os.Getenv("STATIONCTL_TOKEN"); v != "" {
		cfg.token = v
		return
	}
	// читаем сохранённый (stateDir уже создал каталог)
	tf := filepath.Join(stateDir(), "stationctl.token")
	if s := readFirstLine(tf); s != "" {
		cfg.token = s
		return
	}
	// генерим новый — при сбое rand НЕ используем нулевой токен
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		fmt.Fprintln(os.Stderr, "[stationctl] не удалось сгенерировать токен:", err)
		os.Exit(1)
	}
	cfg.token = hex.EncodeToString(b)
	if err := os.WriteFile(tf, []byte(cfg.token+"\n"), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "[stationctl] предупреждение: токен не сохранён в %s: %v\n", tf, err)
		fmt.Fprintln(os.Stderr, "    (он будет меняться при каждом старте — задай STATIONCTL_TOKEN или --token)")
	}
	fmt.Printf("[stationctl] сгенерирован токен (сохранён в %s):\n    %s\n", tf, cfg.token)
}

func localIPs() []string {
	var ips []string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() {
			if ip4 := ipn.IP.To4(); ip4 != nil {
				ips = append(ips, ip4.String())
			}
		}
	}
	return ips
}

func main() {
	flag.StringVar(&cfg.addr, "addr", envOr("STATIONCTL_ADDR", "0.0.0.0:8080"), "адрес:порт для прослушивания")
	flag.StringVar(&cfg.token, "token", "", "токен доступа (пусто = авто-генерация/файл; пустой STATIONCTL_NOAUTH=1 отключает)")
	flag.StringVar(&cfg.mixer, "mixer", envOr("STATIONCTL_MIXER", ""), "имя ALSA-контрола громкости (пусто = автоопределение)")
	flag.StringVar(&cfg.logsDir, "logs", envOr("STATIONCTL_LOGS", ""), "каталог логов (пусто = /data/quasar/daemons_logs)")
	flag.BoolVar(&cfg.shell, "shell", os.Getenv("STATIONCTL_SHELL") == "1", "включить root-шелл через /api/exec")
	flag.StringVar(&cfg.glagolToken, "glagol-token", envOr("STATIONCTL_GLAGOL_TOKEN", ""), "glagol conversationToken (медиа/TTS)")
	flag.StringVar(&cfg.yandexToken, "yandex-token", envOr("STATIONCTL_YANDEX_TOKEN", ""), "аккаунт-токен Яндекса для авто-получения glagol-токена")
	flag.StringVar(&cfg.ledCmd, "led-cmd", envOr("STATIONCTL_LED_CMD", ""), "experimental: команда установки LED, {arg} подставляется")
	flag.StringVar(&cfg.spotifyBin, "spotify-bin", envOr("STATIONCTL_SPOTIFY_BIN", ""), "путь к librespot (пусто = искать в PATH)")
	flag.StringVar(&cfg.spotifyName, "spotify-name", envOr("STATIONCTL_SPOTIFY_NAME", ""), "имя Spotify Connect устройства")
	flag.StringVar(&cfg.spotifyDevice, "spotify-device", envOr("STATIONCTL_SPOTIFY_DEVICE", ""), "ALSA-устройство вывода librespot")
	flag.StringVar(&cfg.spotifyArgs, "spotify-args", envOr("STATIONCTL_SPOTIFY_ARGS", ""), "сырые аргументы librespot (перекрывают дефолт)")
	showVer := flag.Bool("version", false, "показать версию")
	flag.Parse()

	if *showVer {
		fmt.Println("stationctl", version)
		return
	}

	if os.Getenv("STATIONCTL_NOAUTH") == "1" {
		cfg.token = ""
		fmt.Println("[stationctl] ВНИМАНИЕ: аутентификация отключена (STATIONCTL_NOAUTH=1)")
	} else {
		ensureToken()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", apiUI)
	mux.HandleFunc("/api/status", guard(false, apiStatus))
	mux.HandleFunc("/api/volume", guard(false, apiVolume)) // GET+POST; CSRF для POST — внутри
	mux.HandleFunc("/api/mixer", guard(false, apiMixer))
	mux.HandleFunc("/api/beep", guard(true, apiBeep))
	mux.HandleFunc("/api/led", guard(false, apiLED))
	mux.HandleFunc("/api/led/set", guard(true, apiLEDSet)) // experimental
	mux.HandleFunc("/api/adb", guard(false, apiADB))       // GET+POST; CSRF для POST — внутри
	mux.HandleFunc("/api/media", guard(true, apiMedia))    // glagol, POST
	mux.HandleFunc("/api/bt", guard(false, apiBT))         // GET+POST
	mux.HandleFunc("/api/files", guard(false, apiFiles))
	mux.HandleFunc("/api/download", guard(false, apiDownload))
	mux.HandleFunc("/api/autostart", guard(false, apiAutostart)) // GET+POST
	mux.HandleFunc("/api/spotify", guard(false, apiSpotify))     // GET+POST (librespot)
	mux.HandleFunc("/api/reboot", guard(true, apiReboot))
	mux.HandleFunc("/api/poweroff", guard(true, apiPoweroff))
	mux.HandleFunc("/api/logs", guard(false, apiLogs))
	mux.HandleFunc("/api/exec", guard(true, apiExec))

	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       30 * time.Second,
		// WriteTimeout не ставим: /api/exec может выполняться до maxExecSeconds,
		// иначе длинная команда завершится, а ответ клиенту не дойдёт.
		IdleTimeout: 120 * time.Second,
	}

	fmt.Printf("[stationctl] v%s слушаю http://%s  (root=%v, shell=%v)\n", version, cfg.addr, os.Geteuid() == 0, cfg.shell)
	for _, ip := range localIPs() {
		port := cfg.addr
		if i := strings.LastIndex(cfg.addr, ":"); i >= 0 {
			port = cfg.addr[i:]
		}
		fmt.Printf("    http://%s%s\n", ip, port)
	}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "[stationctl] сервер остановлен:", err)
		os.Exit(1)
	}
}

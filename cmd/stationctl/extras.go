package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ======================= Медиа / Алиса (glagol :1961) =======================

func apiMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "только POST"})
		return
	}
	if !originOK(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "кросс-сайтовый Origin запрещён"})
		return
	}
	var body struct {
		Command string  `json:"command"` // play|pause|stop|next|prev|volume|say|ask|ping
		Volume  float64 `json:"volume"`  // 0..100 для command=volume
		Text    string  `json:"text"`    // для say/ask
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "битый JSON"})
		return
	}
	var payload map[string]any
	switch body.Command {
	case "play":
		payload = map[string]any{"command": "play"}
	case "pause", "stop":
		payload = map[string]any{"command": "stop"}
	case "next":
		payload = map[string]any{"command": "next"}
	case "prev":
		payload = map[string]any{"command": "prev"}
	case "ping":
		payload = map[string]any{"command": "ping"}
	case "volume":
		v := body.Volume / 100.0
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		payload = map[string]any{"command": "setVolume", "volume": v}
	case "say":
		if strings.TrimSpace(body.Text) == "" {
			writeJSON(w, 400, map[string]string{"error": "нужен text"})
			return
		}
		payload = map[string]any{"command": "sendText", "text": "повтори за мной: " + body.Text}
	case "ask":
		if strings.TrimSpace(body.Text) == "" {
			writeJSON(w, 400, map[string]string{"error": "нужен text"})
			return
		}
		payload = map[string]any{"command": "sendText", "text": body.Text}
	default:
		writeJSON(w, 400, map[string]string{"error": "неизвестная command"})
		return
	}
	state, err := glagolSend(payload)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "state": state})
}

// ======================= Bluetooth (bluetoothctl) =======================

func btctl(args ...string) (string, error) {
	if !have("bluetoothctl") {
		return "", fmt.Errorf("bluetoothctl не найден")
	}
	out, errOut, err := run(12*time.Second, resolve("bluetoothctl"), args...)
	if err != nil && strings.TrimSpace(out) == "" {
		return out, fmt.Errorf("%v %s", err, strings.TrimSpace(errOut))
	}
	return out, nil
}

type btDevice struct {
	MAC  string `json:"mac"`
	Name string `json:"name"`
}

func btParseDevices(out string) []btDevice {
	var ds []btDevice
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Fields(strings.TrimSpace(ln))
		// "Device AA:BB:CC:DD:EE:FF Имя ..."
		if len(f) >= 3 && f[0] == "Device" {
			ds = append(ds, btDevice{MAC: f[1], Name: strings.Join(f[2:], " ")})
		}
	}
	return ds
}

func apiBT(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		powered := false
		if show, err := btctl("show"); err == nil {
			powered = strings.Contains(show, "Powered: yes")
		}
		paired, _ := btctl("devices", "Paired")
		connected, _ := btctl("devices", "Connected")
		writeJSON(w, 200, map[string]any{
			"available": have("bluetoothctl"),
			"powered":   powered,
			"paired":    btParseDevices(paired),
			"connected": btParseDevices(connected),
		})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "только GET или POST"})
		return
	}
	if !originOK(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "кросс-сайтовый Origin запрещён"})
		return
	}
	var body struct {
		Action string `json:"action"` // power_on|power_off|scan|connect|disconnect|pair|remove
		MAC    string `json:"mac"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	needMAC := map[string]bool{"connect": true, "disconnect": true, "pair": true, "remove": true}
	if needMAC[body.Action] && !isMAC(body.MAC) {
		writeJSON(w, 400, map[string]string{"error": "нужен корректный mac"})
		return
	}
	var out string
	var err error
	switch body.Action {
	case "power_on":
		out, err = btctl("power", "on")
	case "power_off":
		out, err = btctl("power", "off")
	case "scan":
		out, err = btctl("--timeout", "8", "scan", "on")
	case "connect":
		out, err = btctl("connect", body.MAC)
	case "disconnect":
		out, err = btctl("disconnect", body.MAC)
	case "pair":
		out, err = btctl("pair", body.MAC)
	case "remove":
		out, err = btctl("remove", body.MAC)
	default:
		writeJSON(w, 400, map[string]string{"error": "неизвестный action"})
		return
	}
	resp := map[string]any{"out": strings.TrimSpace(out)}
	if err != nil {
		resp["error"] = err.Error()
		writeJSON(w, 500, resp)
		return
	}
	writeJSON(w, 200, resp)
}

func isMAC(s string) bool {
	parts := strings.Split(s, ":")
	if len(parts) != 6 {
		return false
	}
	for _, p := range parts {
		if len(p) != 2 {
			return false
		}
		for _, c := range p {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

// ======================= Файлы (просмотр/скачивание под белым списком) =======================

// fileRoots — куда разрешено заглядывать (read-only).
func fileRoots() []string {
	roots := []string{"/data", logDir(), quasarDir()}
	// уникализируем
	seen := map[string]bool{}
	var out []string
	for _, r := range roots {
		c := filepath.Clean(r)
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

func pathAllowed(p string) bool {
	c := filepath.Clean(p)
	for _, root := range fileRoots() {
		if c == root || strings.HasPrefix(c, root+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

type fileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	IsDir bool   `json:"is_dir"`
}

func apiFiles(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		writeJSON(w, 200, map[string]any{"roots": fileRoots()})
		return
	}
	p = filepath.Clean(p)
	if !pathAllowed(p) {
		writeJSON(w, 403, map[string]string{"error": "путь вне белого списка (разрешено: /data, логи, vendor/quasar)"})
		return
	}
	fi, err := os.Stat(p)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": err.Error()})
		return
	}
	if !fi.IsDir() {
		writeJSON(w, 400, map[string]string{"error": "это файл — используй /api/download"})
		return
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	var list []fileEntry
	for _, e := range entries {
		info, _ := e.Info()
		var sz int64
		if info != nil {
			sz = info.Size()
		}
		list = append(list, fileEntry{
			Name: e.Name(), Path: filepath.Join(p, e.Name()), Size: sz, IsDir: e.IsDir(),
		})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].IsDir != list[j].IsDir {
			return list[i].IsDir
		}
		return list[i].Name < list[j].Name
	})
	writeJSON(w, 200, map[string]any{"path": p, "entries": list})
}

func apiDownload(w http.ResponseWriter, r *http.Request) {
	p := filepath.Clean(r.URL.Query().Get("path"))
	if !pathAllowed(p) {
		http.Error(w, "путь запрещён", http.StatusForbidden)
		return
	}
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		http.Error(w, "не файл", http.StatusNotFound)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filepath.Base(p)+"\"")
	http.ServeContent(w, r, filepath.Base(p), fi.ModTime(), f) // сам ставит Content-Length/Range
}

// ======================= Автозапуск (@reboot crontab) =======================

const cronTag = "# stationctl-autostart"

func selfExe() string {
	if e, err := os.Executable(); err == nil {
		return e
	}
	return "/data/local/bin/stationctl"
}

func cronLine() string {
	args := "--addr " + cfg.addr
	if cfg.shell {
		args += " --shell"
	}
	return fmt.Sprintf("@reboot %s %s >/data/local/var/stationctl.log 2>&1 %s", selfExe(), args, cronTag)
}

func readCrontab() string {
	out, _, _ := run(5*time.Second, resolve("crontab"), "-l")
	return out
}

func autostartInstalled() bool {
	return strings.Contains(readCrontab(), cronTag)
}

func apiAutostart(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, 200, map[string]any{
			"available": have("crontab"),
			"installed": autostartInstalled(),
			"line":      cronLine(),
		})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "только GET или POST"})
		return
	}
	if !originOK(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "кросс-сайтовый Origin запрещён"})
		return
	}
	if !have("crontab") {
		writeJSON(w, 501, map[string]string{"error": "crontab не найден"})
		return
	}
	var body struct {
		Action string `json:"action"` // install|uninstall
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	// текущие строки без наших
	var keep []string
	for _, ln := range strings.Split(readCrontab(), "\n") {
		if strings.TrimSpace(ln) == "" || strings.Contains(ln, cronTag) {
			continue
		}
		keep = append(keep, ln)
	}
	switch body.Action {
	case "install":
		keep = append(keep, cronLine())
	case "uninstall":
		// уже отфильтровали
	default:
		writeJSON(w, 400, map[string]string{"error": "action: install|uninstall"})
		return
	}
	newTab := strings.Join(keep, "\n") + "\n"
	if err := writeCrontab(newTab); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"installed": body.Action == "install"})
}

// writeCrontab загружает новый crontab через stdin `crontab -`.
func writeCrontab(content string) error {
	tmp := filepath.Join(stateDir(), "crontab.tmp")
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	defer os.Remove(tmp)
	_, errOut, err := run(5*time.Second, resolve("crontab"), tmp)
	if err != nil {
		return fmt.Errorf("crontab: %v %s", err, strings.TrimSpace(errOut))
	}
	return nil
}

// ======================= LED (experimental) =======================

func apiLEDSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "только POST"})
		return
	}
	if !originOK(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "кросс-сайтовый Origin запрещён"})
		return
	}
	if cfg.ledCmd == "" {
		writeJSON(w, 501, map[string]string{
			"error": "LED-команда не задана. На этой прошивке интерфейс LED проприетарный; " +
				"задай рабочую команду через --led-cmd (в ней {arg} заменяется значением).",
		})
		return
	}
	var body struct {
		Arg string `json:"arg"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	cmdStr := strings.ReplaceAll(cfg.ledCmd, "{arg}", body.Arg)
	out, errOut, err := run(8*time.Second, resolve("sh"), "-c", cmdStr)
	resp := map[string]any{"ran": cmdStr, "out": strings.TrimSpace(out)}
	if errOut != "" {
		resp["stderr"] = strings.TrimSpace(errOut)
	}
	if err != nil {
		resp["error"] = err.Error()
		writeJSON(w, 500, resp)
		return
	}
	writeJSON(w, 200, resp)
}

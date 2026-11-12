package main

// Spotify Connect через librespot. stationctl НЕ содержит librespot — он им
// управляет: запускает/останавливает процесс, показывает статус и лог, помогает
// выбрать ALSA-устройство. Сам бинарь librespot (aarch64) ставится отдельно
// (apkg или ручной push) — см. README.
//
// ⚠️ EXPERIMENTAL: аудио-тракт станции принадлежит стеку Алисы (mediad). librespot
// в ALSA может конфликтовать за устройство — отсюда выбор --device и возможная
// необходимость поставить воспроизведение Алисы на паузу. Проверяется на железе.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// procMgr — простой менеджер одного долгоживущего дочернего процесса.
type procMgr struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	startedAt time.Time
	logPath   string
	args      []string
}

var spotify procMgr

func librespotBin() (string, bool) {
	if cfg.spotifyBin != "" {
		if fileExists(cfg.spotifyBin) {
			return cfg.spotifyBin, true
		}
		return cfg.spotifyBin, false
	}
	return lookup("librespot")
}

// buildSpotifyArgs собирает аргументы librespot (или берёт сырые из --spotify-args).
func buildSpotifyArgs(name, device string, bitrate, vol int) []string {
	if strings.TrimSpace(cfg.spotifyArgs) != "" {
		return strings.Fields(cfg.spotifyArgs)
	}
	if name == "" {
		h, _ := os.Hostname()
		name = "Yandex " + h
	}
	if bitrate == 0 {
		bitrate = 160
	}
	args := []string{
		"-n", name,
		"--backend", "alsa",
		"--bitrate", strconv.Itoa(bitrate),
		"--disable-audio-cache",
	}
	if vol > 0 {
		args = append(args, "--initial-volume", strconv.Itoa(vol))
	}
	if device != "" {
		args = append(args, "--device", device)
	}
	return args
}

func (m *procMgr) running() (bool, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != nil && m.cmd.Process != nil {
		// жив ли процесс (signal 0)
		if err := m.cmd.Process.Signal(sig0()); err == nil {
			return true, m.cmd.Process.Pid
		}
	}
	return false, 0
}

func (m *procMgr) start(name, device string, bitrate, vol int) error {
	bin, ok := librespotBin()
	if !ok {
		return fmt.Errorf("librespot не найден (поставь пакет или задай --spotify-bin)")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != nil && m.cmd.Process != nil {
		if err := m.cmd.Process.Signal(sig0()); err == nil {
			return fmt.Errorf("librespot уже запущен (pid %d)", m.cmd.Process.Pid)
		}
	}
	args := buildSpotifyArgs(name, device, bitrate, vol)
	logPath := filepath.Join(stateDir(), "librespot.log")
	lf, err := os.Create(logPath)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.Env = augmentedEnv()
	setPgid(cmd)
	if err := cmd.Start(); err != nil {
		lf.Close()
		return err
	}
	m.cmd = cmd
	m.startedAt = time.Now()
	m.logPath = logPath
	m.args = args
	// reaper: дождаться выхода, закрыть лог, снять ссылку
	go func(c *exec.Cmd, f *os.File) {
		_ = c.Wait()
		f.Close()
		m.mu.Lock()
		if m.cmd == c {
			m.cmd = nil
		}
		m.mu.Unlock()
	}(cmd, lf)
	return nil
}

func (m *procMgr) stop() error {
	m.mu.Lock()
	c := m.cmd
	m.mu.Unlock()
	if c == nil || c.Process == nil {
		// добьём внешний librespot, если есть
		if pid, ok := pidof("librespot"); ok {
			if p, err := os.FindProcess(pid); err == nil {
				killTree(p)
				return nil
			}
		}
		return fmt.Errorf("librespot не запущен")
	}
	killTree(c.Process)
	return nil
}

// alsaDevices — список устройств вывода (aplay -L), сокращённо.
func alsaDevices() []string {
	if !have("aplay") {
		return nil
	}
	out, _, err := run(4*time.Second, resolve("aplay"), "-L")
	if err != nil {
		return nil
	}
	var devs []string
	for _, ln := range strings.Split(out, "\n") {
		if ln == "" || strings.HasPrefix(ln, " ") || strings.HasPrefix(ln, "\t") {
			continue // строки-описания идут с отступом — пропускаем
		}
		devs = append(devs, strings.TrimSpace(ln))
		if len(devs) >= 30 {
			break
		}
	}
	return devs
}

func apiSpotify(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		bin, present := librespotBin()
		run, pid := spotify.running()
		resp := map[string]any{
			"present":      present,
			"bin":          bin,
			"running":      run,
			"pid":          pid,
			"alsa_devices": alsaDevices(),
		}
		spotify.mu.Lock()
		if !spotify.startedAt.IsZero() && run {
			resp["uptime_sec"] = int(time.Since(spotify.startedAt).Seconds())
		}
		resp["args"] = spotify.args
		lp := spotify.logPath
		spotify.mu.Unlock()
		if lp != "" {
			if t, err := tailPath(lp, 40); err == nil {
				resp["log"] = t
			}
		}
		writeJSON(w, 200, resp)
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
		Action  string `json:"action"` // start|stop|restart
		Name    string `json:"name"`
		Device  string `json:"device"`
		Bitrate int    `json:"bitrate"`
		Volume  int    `json:"volume"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := body.Name
	if name == "" {
		name = cfg.spotifyName
	}
	device := body.Device
	if device == "" {
		device = cfg.spotifyDevice
	}
	switch body.Action {
	case "stop":
		if err := spotify.stop(); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
	case "restart":
		_ = spotify.stop()
		time.Sleep(400 * time.Millisecond)
		if err := spotify.start(name, device, body.Bitrate, body.Volume); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
	case "start":
		if err := spotify.start(name, device, body.Bitrate, body.Volume); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
	default:
		writeJSON(w, 400, map[string]string{"error": "action: start|stop|restart"})
		return
	}
	run, pid := spotify.running()
	writeJSON(w, 200, map[string]any{"running": run, "pid": pid})
}

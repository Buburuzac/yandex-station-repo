package main

// Локальный клиент glagol (:1961) — медиа-управление и отправка текста Алисе.
// Протокол: TLS + WebSocket (RFC6455), сообщения JSON с conversationToken.
// Сервер крутится НА станции, поэтому ходим на 127.0.0.1:1961 (серт самоподписанный).
//
// ⚠️ EXPERIMENTAL: собрать/проверить на железе не удалось. Нужен glagol-токен
// (--glagol-token / STATIONCTL_GLAGOL_TOKEN / файл), либо аккаунт-токен Яндекса
// для авто-получения (--yandex-token). Ручная реализация WS — вычитана, но
// не прогнана; при проблемах смотри wss-кадры в логах.

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const glagolPort = "1961"

// glagolToken добывает conversationToken: прямой токен или через аккаунт-токен.
func glagolToken() (string, error) {
	if cfg.glagolToken != "" {
		return cfg.glagolToken, nil
	}
	if cfg.yandexToken == "" {
		return "", errors.New("нет glagol-токена (--glagol-token) и аккаунт-токена (--yandex-token) для авто-получения")
	}
	devID := deviceID()
	plat := platform()
	if devID == "" {
		return "", errors.New("не удалось определить device_id для запроса токена")
	}
	u := "https://quasar.yandex.net/glagol/token?" + url.Values{
		"device_id": {devID}, "platform": {plat},
	}.Encode()
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Authorization", "OAuth "+cfg.yandexToken)
	cl := &http.Client{Timeout: 10 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Token  string `json:"token"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", fmt.Errorf("бэкенд не вернул токен (status=%s)", out.Status)
	}
	return out.Token, nil
}

// wsConn — минимальный WebSocket-клиент поверх готового io-соединения.
type wsConn struct {
	c  net.Conn
	br *bufio.Reader
}

func wsDial(host string) (*wsConn, error) {
	d := &net.Dialer{Timeout: 5 * time.Second}
	raw, err := tls.DialWithDialer(d, "tcp", net.JoinHostPort(host, glagolPort),
		&tls.Config{InsecureSkipVerify: true}) // серт станции самоподписанный
	if err != nil {
		return nil, err
	}
	// handshake
	key := make([]byte, 16)
	_, _ = rand.Read(key)
	k := base64.StdEncoding.EncodeToString(key)
	req := "GET / HTTP/1.1\r\n" +
		"Host: " + host + ":" + glagolPort + "\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + k + "\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := raw.Write([]byte(req)); err != nil {
		raw.Close()
		return nil, err
	}
	br := bufio.NewReader(raw)
	// читаем статус-строку + заголовки
	status, err := br.ReadString('\n')
	if err != nil {
		raw.Close()
		return nil, err
	}
	if !strings.Contains(status, "101") {
		raw.Close()
		return nil, fmt.Errorf("ws upgrade отклонён: %s", strings.TrimSpace(status))
	}
	// проверим Accept
	want := base64.StdEncoding.EncodeToString(sha1Sum(k + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	accOK := false
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			raw.Close()
			return nil, err
		}
		if strings.TrimSpace(line) == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "sec-websocket-accept:") {
			if strings.TrimSpace(line[len("sec-websocket-accept:"):]) == want {
				accOK = true
			}
		}
	}
	if !accOK {
		raw.Close()
		return nil, errors.New("ws: неверный Sec-WebSocket-Accept")
	}
	return &wsConn{c: raw, br: br}, nil
}

func sha1Sum(s string) []byte {
	h := sha1.Sum([]byte(s))
	return h[:]
}

// writeText отправляет текстовый кадр (клиент → сервер обязан маскировать).
func (w *wsConn) writeText(data []byte) error {
	var hdr []byte
	fin := byte(0x81) // FIN + text
	n := len(data)
	switch {
	case n < 126:
		hdr = []byte{fin, byte(0x80 | n)}
	case n < 65536:
		hdr = []byte{fin, 0x80 | 126, byte(n >> 8), byte(n)}
	default:
		hdr = make([]byte, 10)
		hdr[0] = fin
		hdr[1] = 0x80 | 127
		binary.BigEndian.PutUint64(hdr[2:], uint64(n))
	}
	mask := make([]byte, 4)
	_, _ = rand.Read(mask)
	masked := make([]byte, n)
	for i := 0; i < n; i++ {
		masked[i] = data[i] ^ mask[i%4]
	}
	_ = w.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := w.c.Write(hdr); err != nil {
		return err
	}
	if _, err := w.c.Write(mask); err != nil {
		return err
	}
	_, err := w.c.Write(masked)
	return err
}

// readMessage читает один кадр (сервер не маскирует). Склейку фрагментов не делаем —
// glagol шлёт состояние одним text-кадром.
func (w *wsConn) readMessage(deadline time.Time) ([]byte, error) {
	_ = w.c.SetReadDeadline(deadline)
	h := make([]byte, 2)
	if _, err := io.ReadFull(w.br, h); err != nil {
		return nil, err
	}
	opcode := h[0] & 0x0f
	masked := h[1]&0x80 != 0
	ln := int(h[1] & 0x7f)
	switch ln {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(w.br, ext); err != nil {
			return nil, err
		}
		ln = int(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(w.br, ext); err != nil {
			return nil, err
		}
		ln = int(binary.BigEndian.Uint64(ext))
	}
	var mask []byte
	if masked {
		mask = make([]byte, 4)
		if _, err := io.ReadFull(w.br, mask); err != nil {
			return nil, err
		}
	}
	payload := make([]byte, ln)
	if _, err := io.ReadFull(w.br, payload); err != nil {
		return nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	if opcode == 0x8 { // close
		return nil, errors.New("ws закрыт сервером")
	}
	return payload, nil
}

func (w *wsConn) close() { _ = w.c.Close() }

// glagolSend подключается, шлёт одну команду-payload и возвращает первый ответ стейта.
func glagolSend(payload map[string]any) (map[string]any, error) {
	tok, err := glagolToken()
	if err != nil {
		return nil, err
	}
	ws, err := wsDial("127.0.0.1")
	if err != nil {
		return nil, err
	}
	defer ws.close()

	msg := map[string]any{
		"conversationToken": tok,
		"id":                randID(),
		"sentTime":          time.Now().UnixMilli(),
		"payload":           payload,
	}
	b, _ := json.Marshal(msg)
	if err := ws.writeText(b); err != nil {
		return nil, err
	}
	// читаем первый ответ-стейт
	data, err := ws.readMessage(time.Now().Add(6 * time.Second))
	if err != nil {
		return nil, err
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		return map[string]any{"raw": string(data)}, nil
	}
	return state, nil
}

func randID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMessageTransferIsVisibleToRecipient(t *testing.T) {
	app := newTestServer(t)
	sender := registerDevice(t, app, "sender-device", "Sender")
	recipient := registerDevice(t, app, "receiver-device", "Receiver")

	body := `{"targetId":"receiver-device","text":"hello from the network"}`
	req := localRequest(http.MethodPost, "/api/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sender)
	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("send message: status=%d body=%s", res.Code, res.Body.String())
	}

	req = localRequest(http.MethodGet, "/api/transfers", nil)
	req.Header.Set("Authorization", "Bearer "+recipient)
	res = httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("list transfers: status=%d body=%s", res.Code, res.Body.String())
	}

	var payload struct {
		Transfers []Transfer `json:"transfers"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Transfers) != 1 || payload.Transfers[0].Text != "hello from the network" {
		t.Fatalf("unexpected transfers: %#v", payload.Transfers)
	}
}

func TestFileTransferChecksRecipient(t *testing.T) {
	app := newTestServer(t)
	sender := registerDevice(t, app, "sender-device", "Sender")
	recipient := registerDevice(t, app, "receiver-device", "Receiver")
	outsider := registerDevice(t, app, "outsider-device", "Outsider")

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("targetId", "receiver-device"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "report.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("private LAN payload"))
	_ = writer.Close()

	req := localRequest(http.MethodPost, "/api/transfers", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+sender)
	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("upload: status=%d body=%s", res.Code, res.Body.String())
	}

	var transfer Transfer
	if err := json.NewDecoder(res.Body).Decode(&transfer); err != nil {
		t.Fatal(err)
	}

	req = localRequest(http.MethodGet, "/api/transfers/"+transfer.ID+"/download", nil)
	req.SetPathValue("id", transfer.ID)
	req.Header.Set("Authorization", "Bearer "+outsider)
	res = httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("outsider download status=%d, want 403", res.Code)
	}

	req = localRequest(http.MethodGet, "/api/transfers/"+transfer.ID+"/download", nil)
	req.SetPathValue("id", transfer.ID)
	req.Header.Set("Authorization", "Bearer "+recipient)
	res = httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("recipient download: status=%d body=%s", res.Code, res.Body.String())
	}
	data, _ := io.ReadAll(res.Body)
	if string(data) != "private LAN payload" {
		t.Fatalf("downloaded %q", data)
	}
}

func TestCrossOriginMutationIsRejected(t *testing.T) {
	app := newTestServer(t)
	req := localRequest(http.MethodPost, "http://neondrop.local/api/devices/register",
		strings.NewReader(`{"id":"browser-device","name":"Browser","kind":"laptop"}`))
	req.Host = "neondrop.local"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://attacker.example")
	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", res.Code)
	}
}

func TestPublicRemoteAddressIsRejected(t *testing.T) {
	app := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.RemoteAddr = "203.0.113.10:43210"
	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", res.Code)
	}
}

func TestExistingDeviceCannotBeClaimedWithoutSession(t *testing.T) {
	app := newTestServer(t)
	token := registerDevice(t, app, "browser-device", "Browser")

	req := localRequest(http.MethodPost, "/api/devices/register",
		strings.NewReader(`{"id":"browser-device","name":"Impostor","kind":"phone"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusConflict {
		t.Fatalf("unauthenticated claim status=%d, want 409", res.Code)
	}

	req = localRequest(http.MethodPost, "/api/devices/register",
		strings.NewReader(`{"id":"browser-device","name":"Renamed Browser","kind":"laptop"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	res = httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("authenticated update status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestConnectionCountKeepsDeviceOnline(t *testing.T) {
	app := newTestServer(t)
	registerDevice(t, app, "browser-device", "Browser")

	first := make(chan event, 1)
	second := make(chan event, 1)
	app.subscribe("browser-device", first)
	app.subscribe("browser-device", second)
	app.unsubscribe("browser-device", first)

	devices := app.deviceSnapshot()
	if len(devices) != 1 || !devices[0].Online {
		t.Fatalf("device should remain online while one stream is connected: %#v", devices)
	}

	app.unsubscribe("browser-device", second)
	devices = app.deviceSnapshot()
	if devices[0].Online {
		t.Fatal("device should be offline after all streams disconnect")
	}
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	app, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app
}

func registerDevice(t *testing.T, app *Server, id, name string) string {
	t.Helper()
	body := `{"id":"` + id + `","name":"` + name + `","kind":"laptop"}`
	req := localRequest(http.MethodPost, "/api/devices/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("register %s: status=%d body=%s", id, res.Code, res.Body.String())
	}
	for _, cookie := range res.Result().Cookies() {
		if cookie.Name == sessionCookie {
			return cookie.Value
		}
	}
	t.Fatal("registration did not set session cookie")
	return ""
}

func localRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.RemoteAddr = "127.0.0.1:12345"
	return req
}

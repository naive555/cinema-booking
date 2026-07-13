// Integration tests for HTTP API correctness.
// Requires: docker compose up with DEV_AUTH=true and SEED_ON_START=true.
// Run: go test -v -count=1 ./test/...   (from backend/)
package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// ── helpers (mintToken, findAvailableSeat, doSelect, doPay already in concurrency_test.go) ──

func mintAdminToken(t *testing.T, email string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "role": "ADMIN"})
	resp, err := http.Post(testBaseURL()+"/dev/token", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("mintAdminToken %s: %v", email, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mintAdminToken %s: status %d — is DEV_AUTH=true running?", email, resp.StatusCode)
	}
	var out struct{ Token string `json:"token"` }
	json.NewDecoder(resp.Body).Decode(&out)
	return out.Token
}

func getJSON(t *testing.T, path, token string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("GET", testBaseURL()+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

// ── Booking correctness ───────────────────────────────────────────────────────

// TestSelectOnBookedSeat_Returns409 books a seat fully, then proves a second
// select attempt on the same seat is rejected with 409.
func TestSelectOnBookedSeat_Returns409(t *testing.T) {
	const showtimeID = "000000000000000000000001"
	tok := mintToken(t, "booker-dup@apitest.com")

	seatID, _, _ := findAvailableSeat(t, tok, showtimeID)

	// Book the seat.
	ownerToken, status := doSelect(tok, showtimeID, seatID)
	if status != http.StatusOK {
		t.Fatalf("select: want 200, got %d", status)
	}
	if doPay(tok, showtimeID, seatID, ownerToken) != http.StatusOK {
		t.Fatalf("pay: want 200")
	}

	// A second select on the same (now BOOKED) seat must return 409.
	tok2 := mintToken(t, "booker-dup2@apitest.com")
	_, status2 := doSelect(tok2, showtimeID, seatID)
	if status2 != http.StatusConflict {
		t.Errorf("second select on booked seat: want 409, got %d", status2)
	}
}

// TestPayWrongOwnerToken_Returns410 proves that a pay request carrying a token
// that does not match the current lock owner is rejected with 410.
func TestPayWrongOwnerToken_Returns410(t *testing.T) {
	const showtimeID = "000000000000000000000001"
	tok := mintToken(t, "pay-wrong@apitest.com")

	seatID, _, _ := findAvailableSeat(t, tok, showtimeID)

	_, status := doSelect(tok, showtimeID, seatID)
	if status != http.StatusOK {
		t.Fatalf("select: want 200, got %d", status)
	}

	// Pay with a fabricated (wrong) ownerToken.
	got := doPay(tok, showtimeID, seatID, "00000000-0000-0000-0000-000000000000")
	if got != http.StatusGone {
		t.Errorf("pay with wrong token: want 410, got %d", got)
	}
}

// TestRelease_ThenReselect proves that an explicit release frees the seat
// immediately — a different user can select it right away rather than waiting
// out the lock TTL.
func TestRelease_ThenReselect(t *testing.T) {
	const showtimeID = "000000000000000000000001"
	tok := mintToken(t, "release-user@apitest.com")

	seatID, _, _ := findAvailableSeat(t, tok, showtimeID)

	ownerToken, status := doSelect(tok, showtimeID, seatID)
	if status != http.StatusOK {
		t.Fatalf("select: want 200, got %d", status)
	}

	released, relStatus := doRelease(tok, showtimeID, seatID, ownerToken)
	if relStatus != http.StatusOK {
		t.Fatalf("release: want 200, got %d", relStatus)
	}
	if !released {
		t.Fatal("release: want released=true")
	}

	tok2 := mintToken(t, "release-user2@apitest.com")
	_, status2 := doSelect(tok2, showtimeID, seatID)
	if status2 != http.StatusOK {
		t.Errorf("re-select after release: want 200, got %d", status2)
	}
}

// TestRelease_WrongToken_DoesNotRelease proves that releasing with a token
// that doesn't own the lock is a no-op — the seat stays held and a second
// select on it is rejected with 409.
func TestRelease_WrongToken_DoesNotRelease(t *testing.T) {
	const showtimeID = "000000000000000000000001"
	tok := mintToken(t, "release-wrong@apitest.com")

	seatID, _, _ := findAvailableSeat(t, tok, showtimeID)

	_, status := doSelect(tok, showtimeID, seatID)
	if status != http.StatusOK {
		t.Fatalf("select: want 200, got %d", status)
	}

	released, relStatus := doRelease(tok, showtimeID, seatID, "00000000-0000-0000-0000-000000000000")
	if relStatus != http.StatusOK {
		t.Fatalf("release with wrong token: want 200, got %d", relStatus)
	}
	if released {
		t.Fatal("release with wrong token: want released=false")
	}

	tok2 := mintToken(t, "release-wrong2@apitest.com")
	_, status2 := doSelect(tok2, showtimeID, seatID)
	if status2 != http.StatusConflict {
		t.Errorf("select on still-held seat: want 409, got %d", status2)
	}
}

// ── Role enforcement ──────────────────────────────────────────────────────────

// TestAdminBookings_UserToken_Returns403 proves the admin endpoint is unreachable
// with a USER-role JWT.
func TestAdminBookings_UserToken_Returns403(t *testing.T) {
	tok := mintToken(t, "mere-user@apitest.com")
	status, _ := getJSON(t, "/api/admin/bookings", tok)
	if status != http.StatusForbidden {
		t.Errorf("admin/bookings with USER token: want 403, got %d", status)
	}
}

func TestAdminAuditLogs_UserToken_Returns403(t *testing.T) {
	tok := mintToken(t, "mere-user2@apitest.com")
	status, _ := getJSON(t, "/api/admin/audit-logs", tok)
	if status != http.StatusForbidden {
		t.Errorf("admin/audit-logs with USER token: want 403, got %d", status)
	}
}

// TestAdminBookings_AdminToken_Returns200 proves an ADMIN JWT can reach the endpoint.
func TestAdminBookings_AdminToken_Returns200(t *testing.T) {
	tok := mintAdminToken(t, "real-admin@apitest.com")
	status, body := getJSON(t, "/api/admin/bookings", tok)
	if status != http.StatusOK {
		t.Errorf("admin/bookings with ADMIN token: want 200, got %d", status)
	}
	if _, ok := body["bookings"]; !ok {
		t.Errorf("response missing 'bookings' key: %v", body)
	}
}

// TestAdminBookings_MovieFilter_Returns200 proves the movieTitle filter is accepted.
func TestAdminBookings_MovieFilter_Returns200(t *testing.T) {
	tok := mintAdminToken(t, "filter-admin@apitest.com")
	status, body := getJSON(t, "/api/admin/bookings?movieTitle=Inception", tok)
	if status != http.StatusOK {
		t.Errorf("admin/bookings?movieTitle=Inception: want 200, got %d", status)
	}
	t.Logf("Inception bookings total=%v", body["total"])
}

// TestProtectedRoute_NoToken_Returns401 proves the auth middleware rejects missing tokens.
func TestProtectedRoute_NoToken_Returns401(t *testing.T) {
	req, _ := http.NewRequest("GET", testBaseURL()+"/api/showtimes", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/showtimes: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: want 401, got %d", resp.StatusCode)
	}
}

// TestHealthz ensures the health endpoint is reachable and all deps are up.
func TestHealthz(t *testing.T) {
	resp, err := http.Get(fmt.Sprintf("%s/healthz", testBaseURL()))
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz: want 200, got %d", resp.StatusCode)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	for _, key := range []string{"mongo", "redis", "status"} {
		if body[key] != "ok" {
			t.Errorf("healthz[%q] = %q, want \"ok\"", key, body[key])
		}
	}
}

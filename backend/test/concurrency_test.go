// Package test contains integration tests that run against the live Docker stack.
// Requires: docker compose up with DEV_AUTH=true and SEED_ON_START=true.
//
// Run:  make test-concurrency   (from repo root)
//
//	or: go test -v -count=1 -run TestConcurrent ./test/...  (from backend/)
package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
)

const concurrency = 50

func testBaseURL() string {
	if u := os.Getenv("TEST_BASE_URL"); u != "" {
		return u
	}
	return "http://localhost:8080"
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

func mintToken(t *testing.T, email string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "role": "USER"})
	resp, err := http.Post(testBaseURL()+"/dev/token", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("mintToken %s: %v", email, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mintToken %s: status %d — is DEV_AUTH=true running?", email, resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.Token
}

// findAvailableSeat returns the first seat in the showtime with status AVAILABLE.
func findAvailableSeat(t *testing.T, token, showtimeID string) (id, row string, number int) {
	t.Helper()
	req, _ := http.NewRequest("GET", testBaseURL()+"/api/showtimes/"+showtimeID+"/seats", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("findAvailableSeat: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Seats []struct {
			ID     string `json:"id"`
			Row    string `json:"row"`
			Number int    `json:"number"`
			Status string `json:"status"`
		} `json:"seats"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	for _, s := range out.Seats {
		if s.Status == "AVAILABLE" {
			return s.ID, s.Row, s.Number
		}
	}
	t.Fatalf("findAvailableSeat: no AVAILABLE seat in showtime %s", showtimeID)
	return "", "", 0
}

// seatStatus fetches the seat-map and returns the status of one specific seat.
func seatStatus(t *testing.T, token, showtimeID, seatID string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", testBaseURL()+"/api/showtimes/"+showtimeID+"/seats", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("seatStatus: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Seats []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"seats"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	for _, s := range out.Seats {
		if s.ID == seatID {
			return s.Status
		}
	}
	t.Fatalf("seatStatus: seat %s not found", seatID)
	return ""
}

func doSelect(token, showtimeID, seatID string) (ownerToken string, status int) {
	req, _ := http.NewRequest("POST",
		fmt.Sprintf("%s/api/showtimes/%s/seats/%s/select", testBaseURL(), showtimeID, seatID),
		nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", -1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode
	}
	var out struct {
		OwnerToken string `json:"ownerToken"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.OwnerToken, http.StatusOK
}

func doPay(token, showtimeID, seatID, ownerToken string) int {
	body, _ := json.Marshal(map[string]string{"ownerToken": ownerToken})
	req, _ := http.NewRequest("POST",
		fmt.Sprintf("%s/api/showtimes/%s/seats/%s/pay", testBaseURL(), showtimeID, seatID),
		bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func doRelease(token, showtimeID, seatID, ownerToken string) (released bool, status int) {
	body, _ := json.Marshal(map[string]string{"ownerToken": ownerToken})
	req, _ := http.NewRequest("DELETE",
		fmt.Sprintf("%s/api/showtimes/%s/seats/%s/select", testBaseURL(), showtimeID, seatID),
		bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, -1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, resp.StatusCode
	}
	var out struct {
		Released bool `json:"released"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.Released, http.StatusOK
}

// ── Scenario 1: 50 goroutines all do select -> pay on the same seat ───────────

// TestConcurrentSelectPay proves that exactly one booking is created when N
// goroutines race to book the same seat simultaneously. The Redis SET NX lock
// ensures at most one holder; the MongoDB unique index is the last-resort guard.
func TestConcurrentSelectPay(t *testing.T) {
	const showtimeID = "000000000000000000000002"

	setupTok := mintToken(t, "setup-sp@conctest.com")
	seatID, row, number := findAvailableSeat(t, setupTok, showtimeID)
	t.Logf("target seat: %s (row %s, seat %d) — showtime %s", seatID, row, number, showtimeID)

	// Pre-mint all tokens sequentially so all goroutines are ready to fire at once.
	tokens := make([]string, concurrency)
	for i := range tokens {
		tokens[i] = mintToken(t, fmt.Sprintf("sp-user%d@conctest.com", i))
	}

	var successes, conflicts int64
	ready := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		tok := tokens[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready // block until all goroutines are spawned, then release simultaneously

			ownerToken, status := doSelect(tok, showtimeID, seatID)
			if status != http.StatusOK {
				atomic.AddInt64(&conflicts, 1)
				return
			}
			// This goroutine holds the lock — try to confirm the booking.
			if doPay(tok, showtimeID, seatID, ownerToken) == http.StatusOK {
				atomic.AddInt64(&successes, 1)
			} else {
				// Lock expired between select and pay (very rare but possible).
				atomic.AddInt64(&conflicts, 1)
			}
		}()
	}

	close(ready) // unleash all goroutines at the same instant
	wg.Wait()

	finalStatus := seatStatus(t, setupTok, showtimeID, seatID)

	t.Logf("━━━ Scenario 1: concurrent select+pay ━━━")
	t.Logf("  goroutines  = %d", concurrency)
	t.Logf("  successes   = %d  ← must be 1", successes)
	t.Logf("  conflicts   = %d  ← must be %d", conflicts, concurrency-1)
	t.Logf("  seat status = %s  ← must be BOOKED", finalStatus)

	if successes != 1 {
		t.Errorf("FAIL: expected exactly 1 success, got %d — double booking occurred!", successes)
	}
	if int(successes+conflicts) != concurrency {
		t.Errorf("FAIL: successes+conflicts=%d, want %d", successes+conflicts, concurrency)
	}
	if finalStatus != "BOOKED" {
		t.Errorf("FAIL: seat %s has status %q, want BOOKED", seatID, finalStatus)
	}
}

// ── Scenario 2: 50 goroutines all SELECT the same seat (no pay) ──────────────

// TestConcurrentSelectOnly proves that the Redis SET NX lock allows at most one
// holder when N goroutines race to acquire a lock on the same seat.
func TestConcurrentSelectOnly(t *testing.T) {
	const showtimeID = "000000000000000000000002"

	setupTok := mintToken(t, "setup-so@conctest.com")
	// findAvailableSeat returns the next AVAILABLE seat — different from the one
	// booked by TestConcurrentSelectPay if that ran first.
	seatID, row, number := findAvailableSeat(t, setupTok, showtimeID)
	t.Logf("target seat: %s (row %s, seat %d) — showtime %s", seatID, row, number, showtimeID)

	tokens := make([]string, concurrency)
	for i := range tokens {
		tokens[i] = mintToken(t, fmt.Sprintf("so-user%d@conctest.com", i))
	}

	var acquired, rejected int64
	ready := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		tok := tokens[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready

			_, status := doSelect(tok, showtimeID, seatID)
			if status == http.StatusOK {
				atomic.AddInt64(&acquired, 1)
			} else {
				atomic.AddInt64(&rejected, 1)
			}
		}()
	}

	close(ready)
	wg.Wait()

	finalStatus := seatStatus(t, setupTok, showtimeID, seatID)

	t.Logf("━━━ Scenario 2: concurrent select-only ━━━")
	t.Logf("  goroutines  = %d", concurrency)
	t.Logf("  acquired    = %d  ← must be 1", acquired)
	t.Logf("  rejected    = %d  ← must be %d", rejected, concurrency-1)
	t.Logf("  seat status = %s  ← must be LOCKED (unpaid hold)", finalStatus)

	if acquired != 1 {
		t.Errorf("FAIL: expected exactly 1 lock acquired, got %d", acquired)
	}
	if int(acquired+rejected) != concurrency {
		t.Errorf("FAIL: acquired+rejected=%d, want %d", acquired+rejected, concurrency)
	}
	if finalStatus != "LOCKED" {
		t.Errorf("FAIL: seat %s has status %q, want LOCKED", seatID, finalStatus)
	}
}

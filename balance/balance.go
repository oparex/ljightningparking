package balance

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var db *sql.DB

func InitDB(dbPath string) error {
	var err error
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS balance_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		balance REAL NOT NULL,
		source TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	return err
}

func Close() {
	if db != nil {
		db.Close()
	}
}

// GetLastBalance returns the most recent balance and whether one exists.
func GetLastBalance() (float64, bool, error) {
	var bal float64
	err := db.QueryRow("SELECT balance FROM balance_log ORDER BY id DESC LIMIT 1").Scan(&bal)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return bal, true, nil
}

func InsertBalance(balance float64, source string) error {
	_, err := db.Exec("INSERT INTO balance_log (balance, source) VALUES (?, ?)", balance, source)
	return err
}

// GetLastBalanceTime returns the timestamp of the most recent balance entry.
// Returns zero time if no rows exist.
func GetLastBalanceTime() (time.Time, error) {
	var createdAt string
	err := db.QueryRow("SELECT created_at FROM balance_log ORDER BY id DESC LIMIT 1").Scan(&createdAt)
	if err == sql.ErrNoRows {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
		"2006-01-02T15:04:05Z",
	} {
		if t, err := time.Parse(layout, createdAt); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse timestamp %q", createdAt)
}

// IsBalanceStale returns true if there is no balance record or the last one is older than maxAge.
func IsBalanceStale(maxAge time.Duration) (bool, error) {
	t, err := GetLastBalanceTime()
	if err != nil {
		return false, err
	}
	if t.IsZero() {
		return true, nil
	}
	return time.Since(t) > maxAge, nil
}

// ParseBalanceCheckSMS parses the response to a "Stanje" inquiry SMS.
// Format: "Stanje na SMS Parking racunu je -1,1 EUR."
func ParseBalanceCheckSMS(content string) (float64, bool) {
	const prefix = "Stanje na SMS Parking racunu je "
	if !strings.HasPrefix(content, prefix) {
		return 0, false
	}
	rest := strings.TrimPrefix(content, prefix)
	rest = strings.TrimSuffix(rest, ".")
	return parseDecimal(rest)
}

// parseDecimal parses Slovenian comma-decimal format like "7,20" to 7.20
func parseDecimal(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", ".")
	s = strings.TrimSuffix(s, " EUR")
	s = strings.TrimSuffix(s, "EUR")
	s = strings.TrimSpace(s)
	var v float64
	n, err := fmt.Sscanf(s, "%f", &v)
	if err != nil || n != 1 {
		return 0, false
	}
	return v, true
}

// ParseSuccessSMS parses a successful parking SMS.
// Returns validUntil, price, balance, and ok.
func ParseSuccessSMS(content string) (string, float64, float64, bool) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || !strings.Contains(lines[0], "uspesno") {
		return "", 0, 0, false
	}

	var validUntil string
	var price, balance float64
	var gotPrice, gotBalance bool

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Veljavnost:") {
			validUntil = strings.TrimSpace(strings.TrimPrefix(line, "Veljavnost:"))
		} else if strings.HasPrefix(line, "Cena:") {
			price, gotPrice = parseDecimal(strings.TrimPrefix(line, "Cena:"))
		} else if strings.HasPrefix(line, "Stanje:") {
			balance, gotBalance = parseDecimal(strings.TrimPrefix(line, "Stanje:"))
		}
	}

	if !gotPrice || !gotBalance {
		return "", 0, 0, false
	}

	return validUntil, price, balance, true
}

// ParseRejectionSMS parses a rejection SMS (insufficient funds).
// Returns balance and ok.
func ParseRejectionSMS(content string) (float64, bool) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || !strings.Contains(lines[0], "zavrnjeno") {
		return 0, false
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Stanje na racunu:") {
			balance, ok := parseDecimal(strings.TrimPrefix(line, "Stanje na racunu:"))
			return balance, ok
		}
	}

	return 0, false
}

package entry

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func NewSecret() (string, []byte) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	v := hex.EncodeToString(b[:])
	h := sha256.Sum256([]byte(v))
	return v, h[:]
}
func VerifySecret(hash []byte, value string) bool {
	h := sha256.Sum256([]byte(value))
	return len(hash) == sha256.Size && subtle.ConstantTimeCompare(hash, h[:]) == 1
}
func DefaultLimits() Limits { return Limits{RunsPerHour: 3, USDPerHour: 5, TaskUSD: 1} }
func ValidateLimits(l Limits) error {
	if l.RunsPerHour < 1 || l.RunsPerHour > MaxRunsPerHour || math.IsNaN(l.USDPerHour) || math.IsInf(l.USDPerHour, 0) || l.USDPerHour <= 0 || l.USDPerHour > MaxUSDPerHour || math.IsNaN(l.TaskUSD) || math.IsInf(l.TaskUSD, 0) || l.TaskUSD < 0.000001 || l.TaskUSD > l.USDPerHour {
		return fmt.Errorf("%w: limits", ErrInvalid)
	}
	return nil
}
func ValidateConnection(c Connection) error {
	if c.ID == "" || c.Tenant == "" || c.User == "" || c.ProjectID == "" || c.Kind == "" || len(c.Name) > 120 || len(c.ID) > 200 {
		return ErrInvalid
	}
	if err := ValidateLimits(c.Limits); err != nil {
		return err
	}
	if len(c.Sinks) == 0 || len(c.Sinks) > 3 {
		return fmt.Errorf("%w: select a delivery destination", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, s := range c.Sinks {
		if seen[s.Sink] {
			return fmt.Errorf("%w: duplicate sink", ErrInvalid)
		}
		seen[s.Sink] = true
		switch s.Sink {
		case "web":
		case "github_comment":
			if (s.Address.Repo == "" || s.Address.Issue < 1) && !(c.Kind == "github_comment" && s.Address.Repo == "" && s.Address.Issue == 0) {
				return fmt.Errorf("%w: GitHub destination", ErrInvalid)
			}
		case "email":
			if !(c.Kind == "email" && s.Address.Email == "") && (!strings.Contains(s.Address.Email, "@") || strings.ContainsAny(s.Address.Email, "\r\n")) {
				return fmt.Errorf("%w: email destination", ErrInvalid)
			}
		default:
			return fmt.Errorf("%w: sink", ErrInvalid)
		}
		for _, p := range s.Phases {
			switch p {
			case Accepted, Question, Result, Failed:
			default:
				return fmt.Errorf("%w: phase", ErrInvalid)
			}
		}
	}
	return nil
}
func (s Schema) Validate(config map[string]any) error {
	known := map[string]bool{}
	for _, f := range s.Fields {
		known[f.Name] = true
		v, ok := config[f.Name]
		if !ok || v == nil {
			if f.Required {
				return fmt.Errorf("%w: %s required", ErrInvalid, f.Name)
			}
			continue
		}
		switch f.Type {
		case "string", "text", "select", "cron":
			str, ok := v.(string)
			if !ok || len(str) > 16000 || f.Required && strings.TrimSpace(str) == "" {
				return fmt.Errorf("%w: %s", ErrInvalid, f.Name)
			}
			if len(f.Options) > 0 {
				found := false
				for _, o := range f.Options {
					found = found || str == o
				}
				if !found {
					return fmt.Errorf("%w: %s", ErrInvalid, f.Name)
				}
			}
		case "boolean":
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("%w: %s", ErrInvalid, f.Name)
			}
		case "number", "integer":
			n, ok := number(v)
			if !ok || math.IsNaN(n) || math.IsInf(n, 0) || f.Type == "integer" && n != math.Trunc(n) || f.Min != nil && n < *f.Min || f.Max != nil && n > *f.Max {
				return fmt.Errorf("%w: %s", ErrInvalid, f.Name)
			}
		default:
			return fmt.Errorf("%w: unsupported field type", ErrInvalid)
		}
	}
	for k := range config {
		if !known[k] {
			return fmt.Errorf("%w: unknown field %s", ErrInvalid, k)
		}
	}
	return nil
}
func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		n, e := x.Float64()
		return n, e == nil
	}
	return 0, false
}
func Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 7 {
		attempt = 7
	}
	return 30 * time.Second * time.Duration(1<<uint(attempt-1))
}
func DeliveryKey(connection, trigger string, phase Phase, sink string) string {
	b, _ := json.Marshal([]string{connection, trigger, string(phase), sink})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// FinishDelivery records a definite failure separately from an uncertain send.
func FinishDelivery(d *Delivery, err error, now time.Time) {
	d.UpdatedAt = now
	d.LeaseUntil = time.Time{}
	switch {
	case err == nil:
		d.State = "done"
		d.LastError = ""
	case errors.Is(err, ErrUnknownDelivery):
		d.State = "unknown"
		d.LastError = "delivery outcome unknown"
	case d.Attempts >= MaxAttempts:
		d.State = "dead"
		d.LastError = "delivery failed"
	default:
		d.State = "pending"
		d.NextAt = now.Add(Backoff(d.Attempts))
		d.LastError = "delivery failed"
	}
}

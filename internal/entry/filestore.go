package entry

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// File 是存在一个 JSON 文件里的 Store：只给单进程用（开发、测试、没配库的部署）。触发的租约只在内存里，进程重启就都
// 放开了；投递的租约落盘，重启后看得出哪些发到一半。
type File struct {
	path   string
	mu     sync.Mutex
	leases map[string]time.Time
}

type fileData struct {
	Triggers   []Trigger      `json:"triggers"`
	Deliveries []fileDelivery `json:"deliveries"`
}

type fileDelivery struct {
	Delivery
	LeaseUntil time.Time `json:"lease_until,omitzero"`
}

// NewFile 用 path 这个文件做存储；文件不存在时当作空的。
func NewFile(path string) *File { return &File{path: path, leases: map[string]time.Time{}} }

func (f *File) load() (fileData, error) {
	var d fileData
	raw, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	return d, json.Unmarshal(raw, &d)
}

func (f *File) save(d fileData) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), filepath.Base(f.path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.path)
}

func (f *File) AddTrigger(_ context.Context, t Trigger) (Trigger, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, err := f.load()
	if err != nil {
		return Trigger{}, false, err
	}
	if i := slices.IndexFunc(d.Triggers, func(x Trigger) bool { return x.ID == t.ID }); i >= 0 {
		return d.Triggers[i], false, nil
	}
	d.Triggers = append(d.Triggers, t)
	return t, true, f.save(d)
}

func (f *File) Trigger(_ context.Context, id string) (Trigger, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, err := f.load()
	if err != nil {
		return Trigger{}, false, err
	}
	if i := slices.IndexFunc(d.Triggers, func(x Trigger) bool { return x.ID == id }); i >= 0 {
		return d.Triggers[i], true, nil
	}
	return Trigger{}, false, nil
}

func (f *File) UpdateTrigger(_ context.Context, id string, fn func(*Trigger) bool) (Trigger, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, err := f.load()
	if err != nil {
		return Trigger{}, false, err
	}
	i := slices.IndexFunc(d.Triggers, func(x Trigger) bool { return x.ID == id })
	if i < 0 {
		return Trigger{}, false, nil
	}
	if !fn(&d.Triggers[i]) {
		return d.Triggers[i], false, nil
	}
	return d.Triggers[i], true, f.save(d)
}

func (f *File) OpenTriggers(context.Context) ([]Trigger, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, err := f.load()
	if err != nil {
		return nil, err
	}
	var out []Trigger
	for _, t := range d.Triggers {
		if !t.State.Ended() {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *File) InScope(_ context.Context, scope string, since time.Time) ([]Trigger, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, err := f.load()
	if err != nil {
		return nil, err
	}
	var out []Trigger
	for _, t := range d.Triggers {
		if t.Scope == scope && t.CreatedAt.After(since) {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *File) Claim(_ context.Context, id string, now, until time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if held, ok := f.leases[id]; ok && held.After(now) {
		return false, nil
	}
	f.leases[id] = until
	return true, nil
}

func (f *File) Release(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.leases, id)
	return nil
}

func (f *File) AddDelivery(_ context.Context, x Delivery) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, err := f.load()
	if err != nil {
		return false, err
	}
	if slices.ContainsFunc(d.Deliveries, func(y fileDelivery) bool { return y.Key == x.Key }) {
		return false, nil
	}
	d.Deliveries = append(d.Deliveries, fileDelivery{Delivery: x})
	return true, f.save(d)
}

func (f *File) ClaimDeliveries(_ context.Context, trigger string, now, until time.Time, limit int) ([]Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, err := f.load()
	if err != nil {
		return nil, err
	}
	var out []Delivery
	for i := range d.Deliveries {
		x := &d.Deliveries[i]
		due := x.State == DeliveryPending && !x.NextAt.After(now)
		stale := x.State == DeliverySending && !x.LeaseUntil.After(now)
		if len(out) >= limit || trigger != "" && x.Trigger != trigger || !due && !stale {
			continue
		}
		x.State, x.LeaseUntil = DeliverySending, until
		claimed := x.Delivery
		claimed.Interrupted = stale
		out = append(out, claimed)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, f.save(d)
}

func (f *File) SaveDelivery(_ context.Context, x Delivery) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, err := f.load()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(d.Deliveries, func(y fileDelivery) bool { return y.Key == x.Key })
	if i < 0 {
		return errors.New("没有这条投递")
	}
	d.Deliveries[i] = fileDelivery{Delivery: x}
	return f.save(d)
}

func (f *File) Deliveries(_ context.Context, trigger string) ([]Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, err := f.load()
	if err != nil {
		return nil, err
	}
	var out []Delivery
	for _, x := range d.Deliveries {
		if x.Trigger == trigger {
			out = append(out, x.Delivery)
		}
	}
	return out, nil
}

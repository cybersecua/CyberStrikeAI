package database

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Container represents a deployed Kali container reachable via gsocket.
type Container struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	GSSecret   string     `json:"gsSecret"`
	Hostname   string     `json:"hostname"`
	IPAddress  string     `json:"ipAddress"`
	Tags       string     `json:"tags"`
	IsOnline   bool       `json:"isOnline"`
	LastSeenAt *time.Time `json:"lastSeenAt"`
	CreatedAt  time.Time  `json:"createdAt"`
}

func (db *DB) ListContainers() ([]Container, error) {
	rows, err := db.Query(`
		SELECT id, name, gs_secret, hostname, ip_address, tags, is_online, last_seen_at, created_at
		FROM containers ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Container
	for rows.Next() {
		var c Container
		var lastSeen sql.NullInt64
		var ts int64
		if err := rows.Scan(&c.ID, &c.Name, &c.GSSecret, &c.Hostname, &c.IPAddress,
			&c.Tags, &c.IsOnline, &lastSeen, &ts); err != nil {
			db.logger.Warn("containers: scan row", zap.Error(err))
			continue
		}
		c.CreatedAt = time.Unix(ts, 0)
		if lastSeen.Valid {
			t := time.Unix(lastSeen.Int64, 0)
			c.LastSeenAt = &t
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (db *DB) CreateContainer(name, gsSecret string) (*Container, error) {
	c := &Container{
		ID:        uuid.New().String(),
		Name:      name,
		GSSecret:  gsSecret,
		CreatedAt: time.Now(),
	}
	_, err := db.Exec(`
		INSERT INTO containers (id, name, gs_secret, hostname, ip_address, tags, is_online, last_seen_at, created_at)
		VALUES (?, ?, ?, '', '', '', 0, NULL, ?)`,
		c.ID, c.Name, c.GSSecret, c.CreatedAt.Unix())
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (db *DB) GetContainer(id string) (*Container, error) {
	var c Container
	var lastSeen sql.NullInt64
	var ts int64
	err := db.QueryRow(`
		SELECT id, name, gs_secret, hostname, ip_address, tags, is_online, last_seen_at, created_at
		FROM containers WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &c.GSSecret, &c.Hostname, &c.IPAddress,
			&c.Tags, &c.IsOnline, &lastSeen, &ts)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.CreatedAt = time.Unix(ts, 0)
	if lastSeen.Valid {
		t := time.Unix(lastSeen.Int64, 0)
		c.LastSeenAt = &t
	}
	return &c, nil
}

// GetContainerBySecret looks up a container by its gsocket secret (used by container registration).
func (db *DB) GetContainerBySecret(gsSecret string) (*Container, error) {
	var c Container
	var lastSeen sql.NullInt64
	var ts int64
	err := db.QueryRow(`
		SELECT id, name, gs_secret, hostname, ip_address, tags, is_online, last_seen_at, created_at
		FROM containers WHERE gs_secret = ?`, gsSecret).
		Scan(&c.ID, &c.Name, &c.GSSecret, &c.Hostname, &c.IPAddress,
			&c.Tags, &c.IsOnline, &lastSeen, &ts)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.CreatedAt = time.Unix(ts, 0)
	if lastSeen.Valid {
		t := time.Unix(lastSeen.Int64, 0)
		c.LastSeenAt = &t
	}
	return &c, nil
}

// UpdateContainerStatus is called by the container on boot to record hostname, IP, and online state.
func (db *DB) UpdateContainerStatus(gsSecret, hostname, ip string, online bool) error {
	onlineInt := 0
	if online {
		onlineInt = 1
	}
	_, err := db.Exec(`
		UPDATE containers
		SET hostname = ?, ip_address = ?, is_online = ?, last_seen_at = ?
		WHERE gs_secret = ?`,
		hostname, ip, onlineInt, time.Now().Unix(), gsSecret)
	return err
}

func (db *DB) DeleteContainer(id string) error {
	_, err := db.Exec(`DELETE FROM containers WHERE id = ?`, id)
	return err
}

package db

import "time"

// ExternalAPI 是一条外部接口配置(FOFA/Hunter 等空间测绘 API)。
// Credentials 以 AES-256-GCM 加密落库,本层只搬运密文,加解密在 server 层。
type ExternalAPI struct {
	ID          int64      `json:"id"`
	Provider    string     `json:"provider"`
	Name        string     `json:"name"`
	BaseURL     string     `json:"base_url"`
	Credentials []byte     `json:"-"`
	Enabled     bool       `json:"enabled"`
	LastTestAt  *time.Time `json:"last_test_at,omitempty"`
	LastTestOK  *bool      `json:"last_test_ok,omitempty"`
	QuotaInfo   []byte     `json:"-"`
	CreatedAt   time.Time  `json:"created_at"`
}

const externalAPICols = `id, provider, name, base_url, credentials, enabled, last_test_at, last_test_ok, quota_info, created_at`

func scanExternalAPI(sc interface{ Scan(...any) error }) (*ExternalAPI, error) {
	var a ExternalAPI
	if err := sc.Scan(&a.ID, &a.Provider, &a.Name, &a.BaseURL, &a.Credentials, &a.Enabled, &a.LastTestAt, &a.LastTestOK, &a.QuotaInfo, &a.CreatedAt); err != nil {
		return nil, err
	}
	return &a, nil
}

// ExternalAPIStore operates on the external_apis table.
type ExternalAPIStore struct{ db *DB }

func (d *DB) ExternalAPIs() *ExternalAPIStore { return &ExternalAPIStore{db: d} }

func (s *ExternalAPIStore) Create(provider, name, baseURL string, credentials []byte) (int64, error) {
	var id int64
	err := s.db.QueryRow(`
INSERT INTO external_apis(provider, name, base_url, credentials) VALUES ($1,$2,$3,$4) RETURNING id`,
		provider, name, baseURL, credentials).Scan(&id)
	return id, err
}

func (s *ExternalAPIStore) Get(id int64) (*ExternalAPI, error) {
	a, err := scanExternalAPI(s.db.QueryRow(`SELECT `+externalAPICols+` FROM external_apis WHERE id=$1`, id))
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (s *ExternalAPIStore) List() ([]*ExternalAPI, error) {
	rows, err := s.db.Query(`SELECT ` + externalAPICols + ` FROM external_apis ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ExternalAPI
	for rows.Next() {
		a, err := scanExternalAPI(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdatePartial 部分更新;credentials 为 nil 时保持原密文。
type ExternalAPIPatch struct {
	Name        *string
	BaseURL     *string
	Credentials []byte
	Enabled     *bool
}

func (s *ExternalAPIStore) Update(id int64, p ExternalAPIPatch) error {
	_, err := s.db.Exec(`
UPDATE external_apis SET
  name        = COALESCE($2, name),
  base_url    = COALESCE($3, base_url),
  credentials = COALESCE($4, credentials),
  enabled     = COALESCE($5, enabled)
WHERE id=$1`, id, p.Name, p.BaseURL, p.Credentials, p.Enabled)
	return err
}

func (s *ExternalAPIStore) Delete(id int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM external_apis WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *ExternalAPIStore) RecordTest(id int64, ok bool, quotaInfo []byte) error {
	_, err := s.db.Exec(`
UPDATE external_apis SET last_test_at=now(), last_test_ok=$2, quota_info=COALESCE($3, quota_info) WHERE id=$1`,
		id, ok, quotaInfo)
	return err
}

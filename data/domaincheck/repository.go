package domaincheck

import (
	"database/sql"
	"time"

	"github.com/khinshankhan/nomex/utils"
)

type (
	Repository struct {
		conn *sql.DB
	}

	DomainCheck struct {
		Domain string
		Code   *int
		At     *time.Time
	}
)

func NewRepository(conn *sql.DB) Repository {
	return Repository{
		conn: conn,
	}
}

func (repo Repository) SaveDomainCheck(check DomainCheck) error {
	_, err := repo.conn.Exec(
		"INSERT OR REPLACE INTO checks (domain, code, checked_at) VALUES (?, ?, ?);",
		check.Domain,
		check.Code,
		utils.ToSQLiteDT(check.At),
	)

	return err
}

func unpackDomainCheckRows(rows *sql.Rows) ([]DomainCheck, error) {
	results := make([]DomainCheck, 0)
	for rows.Next() {
		var result DomainCheck
		err := rows.Scan(&result.Domain, &result.Code, &result.At)
		if err != nil {
			return nil, err
		}

		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return results, nil
}

func (repo Repository) GetAllCheckedDomains() ([]DomainCheck, error) {
	rows, err := repo.conn.Query("SELECT domain, code, checked_at FROM checks;")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results, err := unpackDomainCheckRows(rows)
	return results, err
}

func (repo Repository) GetPendingDomains() ([]DomainCheck, error) {
	rows, err := repo.conn.Query("SELECT domain, code, checked_at FROM checks WHERE code IS NULL OR code NOT IN (200,404) ORDER BY domain ASC;")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results, err := unpackDomainCheckRows(rows)
	return results, err
}

func (repo Repository) GetAvailableDomains() ([]DomainCheck, error) {
	rows, err := repo.conn.Query("SELECT domain, code, checked_at FROM checks WHERE code = 404 ORDER BY domain ASC;")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results, err := unpackDomainCheckRows(rows)
	return results, err
}

func (repo Repository) BulkEnsureDomainChecks(domains []string) error {
	tx, err := repo.conn.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO checks(domain) VALUES(?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()

	for _, d := range domains {
		if _, err := stmt.Exec(d); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

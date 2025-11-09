package domainban

import (
	"database/sql"
	"time"

	"github.com/khinshankhan/nomex/utils"
)

type (
	Repository struct {
		conn *sql.DB
	}

	DomainBan struct {
		Domain string
		Reason *string
		At     *time.Time
	}
)

func NewRepository(conn *sql.DB) Repository {
	return Repository{
		conn: conn,
	}
}

func (repo Repository) BanDomain(ban DomainBan) error {
	_, err := repo.conn.Exec(
		"INSERT OR REPLACE INTO banned (domain, reason, ban_at) VALUES (?, ?, ?);",
		ban.Domain,
		ban.Reason,
		utils.ToSQLiteDT(ban.At),
	)

	return err
}

func unpackDomainBanRows(rows *sql.Rows) ([]DomainBan, error) {
	results := make([]DomainBan, 0)
	for rows.Next() {
		var result DomainBan
		err := rows.Scan(&result.Domain, &result.Reason, &result.At)
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

func (repo Repository) GetAllBannedDomains() ([]DomainBan, error) {
	rows, err := repo.conn.Query("SELECT domain, reason, ban_at FROM banned;")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results, err := unpackDomainBanRows(rows)
	return results, err
}

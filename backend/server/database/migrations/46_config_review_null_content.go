package dbmigs

import "github.com/go-pg/migrations/v8"

func init() {
	migrations.MustRegisterTx(func(db migrations.DB) error {
		_, err := db.Exec(`
			ALTER TABLE config_report ALTER COLUMN content DROP NOT NULL;
		`)
		return err
	}, func(db migrations.DB) error {
		_, err := db.Exec(`
		    UPDATE config_report SET content='no issue found' WHERE content IS NULL;
			ALTER TABLE config_report ALTER COLUMN content SET NOT NULL;
        `)
		return err
	})
}

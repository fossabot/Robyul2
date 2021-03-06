package migrations

import (
	"github.com/Seklfreak/Robyul2/helpers"
	rethink "github.com/gorethink/gorethink"
)

func m24_create_table_profile_userdata() {
	CreateTableIfNotExists("profile_userdata")

	rethink.Table("profile_userdata").IndexCreate("userid").Run(helpers.GetDB())
}

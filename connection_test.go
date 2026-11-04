package torm

import (
	"testing"
	"time"

	_ "github.com/thinkoner/torm/driver/mysql"
	_ "github.com/thinkoner/torm/driver/sqlite"
)

var DB *Connection

func init() {
	DB = initDb()
}

func skipNoDB(t *testing.T) {
	t.Helper()
	if DB == nil {
		t.Skip("local mysql not available")
	}
}

func initDb() *Connection {
	dsn := "root:@tcp(127.0.0.1:3306)/"
	driver := "mysql"
	db, err := Open(Config{
		Driver: driver,
		Dsn:    dsn + "?charset=utf8&parseTime=true&timeout=500ms",
	})
	if err != nil {
		return nil
	}
	if err := db.DB.Ping(); err != nil {
		return nil
	}

	if err := db.Statement(`CREATE DATABASE IF NOT EXISTS torm_test`); err != nil {
		return nil
	}

	db, err = Open(Config{
		Driver: driver,
		Dsn:    dsn + "torm_test?charset=utf8&parseTime=true",
	})
	if err != nil {
		panic(err)
	}

	db.Statement(`DROP TABLE IF EXISTS users;`)
	db.Statement(`
CREATE TABLE users (
  id int(11) NOT NULL AUTO_INCREMENT,
  name varchar(255) DEFAULT NULL,
  gender varchar(255) DEFAULT NULL,
  addr varchar(255) DEFAULT NULL,
  birth_date date DEFAULT NULL,
  balance decimal(15,4) DEFAULT '0.0000',
  created_at timestamp NULL DEFAULT NULL,
  updated_at timestamp NULL DEFAULT NULL ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id)
);
`)

	return db
}

type User struct {
	Id        int64  `torm:"primary_key;column:id"`
	Name      string `torm:"column:name"`
	Gender    string `torm:"column:gender"`
	Addr      string
	BirthDate string
	Balance   float64
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (u *User) TableName() string {
	return "users"
}

func TestConnection_SelectOne(t *testing.T) {
	skipNoDB(t)
	var user User
	_, _, err := DB.Insert("insert into users (name, gender) values (?, ?)", "Andrew", "M")
	if err != nil {
		t.Error(err)
	}

	err = DB.SelectOne("select * from users where gender = ?", []interface{}{"M"}, &user)
	if err != nil {
		t.Error(err)
	}

	if user.Gender != "M" {
		t.Error("Expect: user's gender should be ", "M")
	}

	if user.Name != "Andrew" {
		t.Error("Expect: user's name should be ", "Andrew")
	}
}

func TestConnection_Select(t *testing.T) {
	skipNoDB(t)
	var users []User

	DB.Insert("insert into users (name, gender) values (?, ?)", "Andrew", "M")
	DB.Insert("insert into users (name, gender) values (?, ?)", "Boston", "M")

	err := DB.Select("select * from users where gender = ?", []interface{}{"M"}, &users)
	if err != nil {
		t.Error(err)
	}

	if len(users) < 1 {
		t.Error("Expect: user's length should > 1 ")
	}

	for _, user := range users {
		if user.Gender != "M" {
			t.Error("Expect: user's gender should be ", "M")
		}
	}
}

func TestConnection_Insert(t *testing.T) {
	skipNoDB(t)
	insertId, _, err := DB.Insert("insert into users (name, gender) values (?, ?)", "Alaska", "F")
	if err != nil {
		t.Error(err)
	}

	if insertId < 0 {
		t.Error("Expect: insertId should > 0 after insert")
	}
}

func TestConnection_Update(t *testing.T) {
	skipNoDB(t)
	id, _, err := DB.Insert("insert into users (name, gender) values (?, ?)", "Chicago", "M")
	if err != nil {
		t.Error(err)
	}

	_, err = DB.Update("update users set gender = ? where id = ?", "M", id)
	if err != nil {
		t.Error(err)
	}
}

func TestConnection_Delete(t *testing.T) {
	skipNoDB(t)
	id, _, err := DB.Insert("insert into users (name, gender) values (?, ?)", "Columbia", "F")
	if err != nil {
		t.Error(err)
	}

	_, err = DB.Delete("delete from users where id = ?", id)
	if err != nil {
		t.Error(err)
	}
}

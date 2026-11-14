package torm

import (
	"database/sql/driver"
	"reflect"
	"testing"
	"time"
)

// json cast 模型
type AppMeta struct {
	Theme  string `json:"theme"`
	Locale string `json:"locale"`
}

type AppSetting struct {
	ID     int64   `torm:"primary_key;column:id"`
	Name   string  `torm:"column:name"`
	Config AppMeta `torm:"column:config;cast:json"`
}

func TestCastJSONWrite(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("BEGIN", &fakeResult{})
	insertSQL := "insert into `app_setting` (`config`, `name`) values (?, ?)"
	st.On(insertSQL, &fakeResult{Affected: 1, LastID: 1})

	err := conn.Transaction(func(c *Connection) error {
		return c.Create(&AppSetting{Name: "app", Config: AppMeta{Theme: "dark", Locale: "zh"}})
	})
	if err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, insertSQL)
	if len(call.Args) != 2 {
		t.Fatalf("args = %#v", call.Args)
	}
	cfg, ok := call.Args[0].(string)
	if !ok || cfg != `{"theme":"dark","locale":"zh"}` {
		t.Errorf("config binding = %#v", call.Args[0])
	}
}

func TestCastJSONRead(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `app_settings` where `id` = ? limit 1", &fakeResult{
		Columns: []string{"id", "name", "config"},
		Rows:    [][]driver.Value{{int64(1), "app", `{"theme":"light","locale":"en"}`}},
	})

	var s AppSetting
	if err := conn.Table("app_settings").Where("id", "=", 1).First(&s); err != nil {
		t.Fatal(err)
	}
	if s.Config.Theme != "light" || s.Config.Locale != "en" {
		t.Errorf("config = %+v", s.Config)
	}
	if !reflect.DeepEqual(s.Config, AppMeta{Theme: "light", Locale: "en"}) {
		t.Errorf("config = %+v", s.Config)
	}
	var _ = time.Now
}

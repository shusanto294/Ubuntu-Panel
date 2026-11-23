package sites

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/crypto"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

func TestDatabaseVar(t *testing.T) {
	mongo, ok := databaseVar(&store.Database{Engine: "mongodb", Name: "app_x1", Username: "app", Password: "p@ss/word"})
	if !ok || mongo.Name != "MONGODB_URI" ||
		mongo.Value != "mongodb://app:p%40ss%2Fword@127.0.0.1:27017/app_x1?authSource=app_x1" {
		t.Errorf("mongo = %#v", mongo)
	}

	mysql, ok := databaseVar(&store.Database{Engine: "mysql", Name: "app_x1", Username: "app", Password: "pw"})
	if !ok || mysql.Name != "DATABASE_URL" || mysql.Value != "mysql://app:pw@127.0.0.1:3306/app_x1" {
		t.Errorf("mysql = %#v", mysql)
	}
}

func TestConnectionsWinAndRedisDatabasesAreUnique(t *testing.T) {
	s := phpVersionService(t)
	ctx := context.Background()

	cipher, err := crypto.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	s.Store.UseCipher(cipher)

	if err := s.Store.SetSecretSetting("redis_password", "s3cret+/"); err != nil {
		t.Fatal(err)
	}

	first, _ := s.Store.FindSite(ctx, makeSite(t, s.Store, "nextjs", ""))
	first.EnvVars = "REDIS_URL=redis://127.0.0.1:6379\nAPP_SECRET=x"

	db, err := s.ConnectRedis(ctx, first)
	if err != nil || db != 1 {
		t.Fatalf("first site got Redis database %d, %v", db, err)
	}

	otherID, err := s.Store.CreateSite(ctx, &store.Site{Domain: "other.com", Type: "nextjs", RootPath: "/var/www/other.com"})
	if err != nil {
		t.Fatal(err)
	}

	other, _ := s.Store.FindSite(ctx, otherID)
	if db, _ := s.ConnectRedis(ctx, other); db != 2 {
		t.Errorf("second site got Redis database %d, want 2", db)
	}

	vars := s.envFor(ctx, first)

	got := map[string]string{}
	count := 0
	for _, v := range vars {
		got[v.Name] = v.Value
		if v.Name == "REDIS_URL" {
			count++
		}
	}

	if count != 1 || got["REDIS_URL"] != "redis://:s3cret+%2F@127.0.0.1:6379/1" {
		t.Errorf("REDIS_URL = %q (%d times)", got["REDIS_URL"], count)
	}

	if got["APP_SECRET"] != "x" {
		t.Errorf("the operator's own variable was lost: %#v", vars)
	}
}

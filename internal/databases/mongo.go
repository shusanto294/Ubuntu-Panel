package databases

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
	"github.com/shusanto294/ubuntu-panel-go/internal/tasks"
)

// MongoAdmin is the account the panel keeps for itself on MongoDB, and
// MongoSecret is where its password lives. MongoDB has no equivalent of the
// unix-socket root login MariaDB and PostgreSQL give the machine's
// administrator: once authorization is enabled, the only way in is a stored
// password, so the panel generates one and remembers it.
const (
	MongoAdmin  = "panelAdmin"
	MongoSecret = "mongo_root_password"
	MongoPort   = 27017
)

// mongoConf is the config file the packages ship.
const mongoConf = "/etc/mongod.conf"

// jsQuote is a double-quoted JavaScript string literal, for the one value that
// is not an identifier the create form has already validated: the password.
func jsQuote(text string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
		"\r", `\r`,
	)

	return `"` + replacer.Replace(text) + `"`
}

// mongoRun feeds a script to mongosh with the script in a file rather than on
// the command line, the same bargain runSQL makes: these scripts carry
// passwords, and `--eval` puts them where `ps` can read them.
func mongoRun(script string, timeout time.Duration) proc.Result {
	path, cleanup, err := stage(script)
	if err != nil {
		return proc.Result{Code: -1, Output: err.Error()}
	}

	defer cleanup()

	return proc.Run("mongosh --quiet --file "+proc.Quote(path), timeout)
}

// mongoPrelude authenticates the rest of a script as the panel's admin user.
//
// mongosh authenticates when it connects, so this cannot be a flag without
// putting the password on a command line; `db.auth` inside the script does the
// same job from a file nobody else can read. Empty when the panel has no
// password stored — a MongoDB installed before the panel existed, which is
// still running without authorization and does not want an auth call.
func mongoPrelude(st *store.Store) string {
	password := st.SecretSetting(MongoSecret)
	if password == "" {
		return ""
	}

	return fmt.Sprintf("db.getSiblingDB(\"admin\").auth(\"%s\", %s);\n",
		MongoAdmin, jsQuote(password))
}

// mongoAnswering waits for the server to accept connections. `systemctl start`
// returns as soon as the process is up, which is before it is listening, and a
// restart after a config change is slower than that again.
func mongoAnswering() bool {
	for attempt := 0; attempt < 15; attempt++ {
		// ping is one of the few commands a server with authorization enabled
		// answers unauthenticated, which is what makes it the readiness check.
		script := "if (db.getSiblingDB(\"admin\").runCommand({ping: 1}).ok !== 1) { quit(1); }\n"

		if mongoRun(script, 20*time.Second).OK() {
			return true
		}

		time.Sleep(2 * time.Second)
	}

	return false
}

// mongoAuthWorks reports whether the stored password still opens the door.
// Also true, harmlessly, on a server with no authorization enabled yet, where
// auth() against a user that exists succeeds anyway.
func mongoAuthWorks(password string) bool {
	// A refused login is an exception in mongosh and a zero return in the shell
	// it replaced; either has to come out of here as false rather than as a
	// task that stops.
	script := fmt.Sprintf(`try {
  if (db.getSiblingDB("admin").auth("%s", %s) != 1) { quit(1); }
} catch (error) {
  quit(1);
}
`, MongoAdmin, jsQuote(password))

	return mongoRun(script, 30*time.Second).OK()
}

// mongoConfigureFile is the edit to /etc/mongod.conf: authorization on, and a
// listener the rest of the network can reach.
//
// Both keys are edited in place when they are already there and appended when
// they are not, because appending a second `security:` or `net:` block to a
// YAML file produces a file MongoDB refuses to start with. It prints
// `changed` or `unchanged` so the caller only restarts when it has to.
const mongoConfigureFile = `
set -e
conf=/etc/mongod.conf
before=$(sudo md5sum "$conf" | awk '{print $1}')

# Reachable from off the machine. MongoDB ships bound to loopback, which is
# right until the operator wants Compass on their desktop or an application on
# another host — and authorization, set just below, is what makes it safe.
if grep -qE '^[[:space:]]*bindIp:' "$conf"; then
  sudo sed -i 's/^\([[:space:]]*\)bindIp:.*/\1bindIp: 0.0.0.0/' "$conf"
elif grep -qE '^net:' "$conf"; then
  sudo sed -i 's/^net:.*/net:\n  bindIp: 0.0.0.0/' "$conf"
else
  printf '\nnet:\n  port: 27017\n  bindIp: 0.0.0.0\n' | sudo tee -a "$conf" > /dev/null
fi

if grep -qE '^[[:space:]]*authorization:' "$conf"; then
  sudo sed -i 's/^\([[:space:]]*\)authorization:.*/\1authorization: enabled/' "$conf"
elif grep -qE '^security:' "$conf"; then
  sudo sed -i 's/^security:.*/security:\n  authorization: enabled/' "$conf"
else
  printf '\nsecurity:\n  authorization: enabled\n' | sudo tee -a "$conf" > /dev/null
fi

after=$(sudo md5sum "$conf" | awk '{print $1}')

if [ "$before" = "$after" ]; then echo unchanged; else echo changed; fi
`

// mongoOpenPort lets the port through the firewall the panel turned on. Only
// when ufw is running: a machine behind somebody else's firewall has none.
const mongoOpenPort = `
if command -v ufw >/dev/null 2>&1 && sudo ufw status | grep -q 'Status: active'; then
  sudo ufw allow 27017/tcp
fi
`

// EnsureMongoServer leaves MongoDB in the state the panel needs it in: running,
// with the panel's administrator, with authorization enabled, and listening
// where a desktop client can reach it.
//
// Idempotent, and deliberately run from two places — the install, and every
// database creation. The second is what repairs a server installed by an older
// panel, where authorization was switched on but nothing ever taught the
// database pages the password, so every create failed with `Command createUser
// requires authentication`.
func EnsureMongoServer(st *store.Store, runner *tasks.Runner) error {
	say := func(text string) {
		if runner != nil {
			runner.Write(text + "\n")
		}
	}

	password, err := st.RememberSecret(MongoSecret, 28)
	if err != nil || password == "" {
		return errors.New("could not generate a MongoDB administrator password")
	}

	if result := proc.Run("sudo systemctl enable --now mongod", 2*time.Minute); !result.OK() {
		return fmt.Errorf("MongoDB would not start: %s", result.Output)
	}

	if !mongoAnswering() {
		return errors.New("MongoDB is not answering on 127.0.0.1:27017. " +
			"`sudo systemctl status mongod` says why.")
	}

	// The administrator can only be created while the server still accepts
	// unauthenticated writes, which is the state a fresh install is in — before
	// the authorization block below goes into the config. On a server that is
	// already set up, the auth check passes and this is skipped.
	if !mongoAuthWorks(password) {
		script := fmt.Sprintf(`const admin = db.getSiblingDB("admin");
if (admin.getUser("%[1]s")) {
  admin.updateUser("%[1]s", {pwd: %[2]s, roles: [{role: "root", db: "admin"}]});
} else {
  admin.createUser({user: "%[1]s", pwd: %[2]s, roles: [{role: "root", db: "admin"}]});
}
`, MongoAdmin, jsQuote(password))

		if result := mongoRun(script, time.Minute); !result.OK() {
			if strings.Contains(result.Output, "requires authentication") {
				return errors.New("MongoDB has authentication enabled and the password the panel " +
					"has for " + MongoAdmin + " is not accepted. Someone changed it outside the " +
					"panel, or the panel's application key was replaced. Reset it from a mongosh " +
					"session on the server, or reinstall MongoDB from the Apps page.")
			}

			return fmt.Errorf("could not create the MongoDB administrator: %s", result.Output)
		}

		say("Created the MongoDB administrator.")
	}

	result := proc.Run(mongoConfigureFile, time.Minute)
	if !result.OK() {
		return fmt.Errorf("could not configure MongoDB: %s", result.Output)
	}

	if strings.Contains(result.Output, "changed") {
		say("Updated " + mongoConf + ".")

		if restart := proc.Run("sudo systemctl restart mongod", 2*time.Minute); !restart.OK() {
			return fmt.Errorf("MongoDB would not restart after its configuration changed: %s",
				restart.Output)
		}

		if !mongoAnswering() {
			return errors.New("MongoDB did not come back after its configuration changed. " +
				"`sudo systemctl status mongod` says why.")
		}
	}

	proc.Run(mongoOpenPort, time.Minute)

	return nil
}

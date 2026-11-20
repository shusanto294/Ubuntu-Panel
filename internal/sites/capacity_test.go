package sites

import (
	"strings"
	"testing"
)

const ubuntuNginxConf = `user www-data;
worker_processes auto;
pid /run/nginx.pid;
error_log /var/log/nginx/error.log;
include /etc/nginx/modules-enabled/*.conf;

events {
	worker_connections 768;
	# multi_accept on;
}

http {
	sendfile on;
	tcp_nopush on;
	keepalive_timeout 65;
	types_hash_max_size 2048;
}
`

func TestTuneNginxMainRaisesUbuntusStockLimits(t *testing.T) {
	tuned, changed := TuneNginxMain(ubuntuNginxConf)
	if !changed {
		t.Fatal("the stock file was reported as already tuned")
	}

	for _, want := range []string{
		"worker_processes auto;\nworker_rlimit_nofile 65535;\n",
		"\tworker_connections 4096;\n",
		"\tkeepalive_timeout 15;\n",
	} {
		if !strings.Contains(tuned, want) {
			t.Errorf("tuned file lacks %q:\n%s", want, tuned)
		}
	}

	if again, changed := TuneNginxMain(tuned); changed || again != tuned {
		t.Error("tuning an already tuned file changed it again")
	}
}

// Numbers somebody chose are theirs: never lowered, never overwritten.
func TestTuneNginxMainLeavesOperatorChoicesAlone(t *testing.T) {
	conf := "worker_processes 4;\nworker_rlimit_nofile 200000;\nevents {\n    worker_connections 20000;\n}\n" +
		"http {\n    keepalive_timeout 30;\n}\n"

	if tuned, changed := TuneNginxMain(conf); changed {
		t.Errorf("an operator's own limits were changed:\n%s", tuned)
	}
}

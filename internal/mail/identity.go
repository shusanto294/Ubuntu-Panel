package mail

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// identityPlugin is the Roundcube plugin that signs mail with the name the
// panel gave the mailbox.
//
// Writing the name into Roundcube's identities table from the panel only works
// once that table has a row for the address, and Roundcube makes the row the
// first time somebody signs in — so a mailbox named when it was created, which
// is every mailbox, sent its first mail and every one after it as the bare
// address. The plugin closes that from Roundcube's side: it names the identity
// as Roundcube creates it, and corrects it at every sign-in after a rename.
const identityPlugin = "ubuntu_panel_identity"

// webmailNamesFile is every mailbox's name, keyed by address. Inside config/,
// which the webmail vhost refuses to serve; the names are not secret, but there
// is no reason to publish them.
const webmailNamesFile = roundcubeRoot + "/config/panel-names.json"

const identityPluginSource = `<?php
/* Managed by Ubuntu Panel. Changes here are overwritten. */
class ubuntu_panel_identity extends rcube_plugin
{
    function init()
    {
        $this->add_hook('user_create', [$this, 'user_create']);
        $this->add_hook('login_after', [$this, 'login_after']);
    }

    private function name_for($email)
    {
        $file = RCUBE_INSTALL_PATH . 'config/panel-names.json';
        if (!is_readable($file)) {
            return '';
        }

        $names = json_decode((string) file_get_contents($file), true);
        $email = strtolower(trim((string) $email));

        return is_array($names) && isset($names[$email]) ? (string) $names[$email] : '';
    }

    function user_create($args)
    {
        $name = $this->name_for(!empty($args['user_email']) ? $args['user_email'] : $args['user']);
        if ($name !== '') {
            $args['user_name'] = $name;
        }

        return $args;
    }

    function login_after($args)
    {
        $user = rcmail::get_instance()->user;
        $address = $user->get_username();
        $name = $this->name_for($address);
        if ($name === '') {
            return $args;
        }

        foreach ((array) $user->list_identities() as $identity) {
            if (strcasecmp($identity['email'], $address) === 0 && $identity['name'] !== $name) {
                $user->update_identity($identity['identity_id'], ['name' => $name]);
            }
        }

        return $args;
    }
}
`

// writeWebmailNames writes the address → name map the plugin reads. Rewritten
// whenever a mailbox is created, renamed or removed, and with the webmail's
// configuration, so it is never older than the panel's own list.
func (s *Service) writeWebmailNames(ctx context.Context) error {
	if !s.Store.ServiceInstalled("roundcube") {
		return nil
	}

	domains, err := s.Store.EmailDomains(ctx)
	if err != nil {
		return err
	}

	names := map[string]string{}

	for _, domain := range domains {
		accounts, err := s.Store.EmailAccounts(ctx, domain.ID)
		if err != nil {
			return err
		}

		for _, account := range accounts {
			if name := strings.TrimSpace(account.Name); name != "" && account.Status != "deleting" {
				names[strings.ToLower(account.Address(domain.Domain))] = name
			}
		}
	}

	contents, err := json.MarshalIndent(names, "", "  ")
	if err != nil {
		return err
	}

	return proc.PutFile(webmailNamesFile, string(contents)+"\n", true)
}

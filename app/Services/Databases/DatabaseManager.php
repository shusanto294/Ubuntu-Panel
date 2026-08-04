<?php

namespace App\Services\Databases;

use App\Models\ActivityLog;
use App\Models\Database;
use App\Models\Service;
use App\Services\Shell\LocalConnection;
use App\Services\Tasks\Step;
use App\Services\Tasks\TaskRunner;
use App\Support\Settings;
use RuntimeException;
use Throwable;

/**
 * Creates and drops databases and their users across MariaDB, PostgreSQL and MongoDB.
 *
 * MariaDB and PostgreSQL are reached through socket auth (`sudo mysql`,
 * `sudo -u postgres psql`), so no root password is stored for them.
 */
class DatabaseManager
{
    public function __construct(protected Settings $settings) {}

    public function create(Database $database): bool
    {

        if (! Service::installed($database->engine)) {
            $database->update([
                'status' => 'failed',
                'last_error' => $database->engineLabel().' is not installed on this machine.',
            ]);

            return false;
        }

        $log = ActivityLog::record([
            'user_id' => $database->user_id,
            'type' => 'database',
            'action' => 'database.create',
            'status' => 'running',
            'message' => $database->engineLabel().': '.$database->name,
        ]);

        $database->update(['status' => 'creating']);
        $connection = app(LocalConnection::class)->timeout(300);

        try {
            $ok = TaskRunner::for($log, $connection)->run([
                Step::make(
                    'Create '.$database->name,
                    $this->createCommands($database)
                ),
                Step::make('Verify', $this->verifyCommands($database)),
            ]);

            $database->update([
                'status' => $ok ? 'ready' : 'failed',
                'last_error' => $ok ? null : $log->fresh()->message,
            ]);

            return $ok;
        } catch (Throwable $e) {
            $database->update(['status' => 'failed', 'last_error' => $e->getMessage()]);
            $log->forceFill(['status' => 'failed', 'message' => $e->getMessage(), 'finished_at' => now()])->save();

            return false;
        } finally {
            $connection->disconnect();
        }
    }

    public function delete(Database $database): bool
    {

        $log = ActivityLog::record([
            'user_id' => $database->user_id,
            'type' => 'database',
            'action' => 'database.delete',
            'status' => 'running',
            'message' => $database->engineLabel().': '.$database->name,
        ]);

        $database->update(['status' => 'deleting']);
        $connection = app(LocalConnection::class)->timeout(300);

        try {
            $ok = TaskRunner::for($log, $connection)->run([
                Step::make('Drop '.$database->name, $this->dropCommands($database)),
            ]);

            if ($ok) {
                $database->delete();
            } else {
                $database->update(['status' => 'failed', 'last_error' => $log->fresh()->message]);
            }

            return $ok;
        } catch (Throwable $e) {
            $database->update(['status' => 'failed', 'last_error' => $e->getMessage()]);
            $log->forceFill(['status' => 'failed', 'message' => $e->getMessage(), 'finished_at' => now()])->save();

            return false;
        } finally {
            $connection->disconnect();
        }
    }

    /**
     * Create the database inline during a larger task (used by site deployments).
     */
    public function createDuringTask(Database $database, LocalConnection $ssh): string
    {
        $output = '';

        foreach ($this->createCommands($database) as $command) {
            [$result, $code] = $ssh->run($command);
            $output .= $result."\n";

            if ($code !== 0) {
                $database->update(['status' => 'failed', 'last_error' => trim($result)]);

                throw new RuntimeException('Database creation failed: '.trim($result));
            }
        }

        $database->update(['status' => 'ready', 'last_error' => null]);

        return trim($output) ?: "Database {$database->name} created.";
    }

    /**
     * @return array<int, string>
     */
    protected function createCommands(Database $database): array
    {
        $name = $database->name;
        $user = $database->username;
        $password = (string) $database->password;

        return match ($database->engine) {
            'mysql' => [
                sprintf(
                    'sudo mysql -e %s',
                    escapeshellarg(sprintf(
                        'CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET %s COLLATE %s;',
                        $name,
                        $database->charset ?: 'utf8mb4',
                        ($database->charset ?: 'utf8mb4').'_unicode_ci'
                    ))
                ),
                sprintf(
                    'sudo mysql -e %s',
                    escapeshellarg(sprintf(
                        "CREATE USER IF NOT EXISTS '%s'@'localhost' IDENTIFIED BY '%s'; ".
                        "ALTER USER '%1\$s'@'localhost' IDENTIFIED BY '%2\$s'; ".
                        "GRANT ALL PRIVILEGES ON `%3\$s`.* TO '%1\$s'@'localhost'; FLUSH PRIVILEGES;",
                        $user,
                        $password,
                        $name
                    ))
                ),
            ],

            'postgres' => [
                sprintf(
                    'sudo -u postgres psql -v ON_ERROR_STOP=1 -c %s',
                    escapeshellarg(sprintf(
                        "DO \$\$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='%s') ".
                        "THEN CREATE ROLE \"%1\$s\" LOGIN PASSWORD '%s'; ".
                        "ELSE ALTER ROLE \"%1\$s\" WITH LOGIN PASSWORD '%2\$s'; END IF; END \$\$;",
                        $user,
                        $password
                    ))
                ),
                sprintf(
                    'sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname=\'%1$s\'" | grep -q 1 || sudo -u postgres createdb -O %2$s %1$s',
                    $name,
                    $user
                ),
                sprintf(
                    'sudo -u postgres psql -v ON_ERROR_STOP=1 -c %s',
                    escapeshellarg(sprintf('GRANT ALL PRIVILEGES ON DATABASE "%s" TO "%s";', $name, $user))
                ),
            ],

            'mongodb' => [
                sprintf(
                    'mongosh %s --quiet --eval %s',
                    $this->mongoAuthFlags(),
                    escapeshellarg(sprintf(
                        'db = db.getSiblingDB(%s); '.
                        'if (!db.getUser(%s)) { db.createUser({user: %2$s, pwd: %s, roles: [{role: "readWrite", db: %1$s}, {role: "dbAdmin", db: %1$s}]}); } '.
                        'else { db.changeUserPassword(%2$s, %3$s); } '.
                        'db.panel_meta.updateOne({_id: "created"}, {$set: {at: new Date()}}, {upsert: true});',
                        json_encode($name),
                        json_encode($user),
                        json_encode($password)
                    ))
                ),
            ],

            default => throw new RuntimeException("Unsupported engine {$database->engine}."),
        };
    }

    /**
     * @return array<int, string>
     */
    protected function verifyCommands(Database $database): array
    {
        return match ($database->engine) {
            // Quoted once, like every other command here. Wrapping the whole
            // thing in double quotes *and* escaping the inner argument nests
            // the quoting and hands mysql a backslash it cannot parse.
            //
            // `SHOW DATABASES LIKE` also exits 0 when it matches nothing, so
            // it confirmed the creation of databases that were never created;
            // this asks a question whose empty answer is a failure.
            'mysql' => [sprintf(
                'sudo mysql -N -e %s | grep -q .',
                escapeshellarg(sprintf(
                    "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = '%s';",
                    $database->name
                ))
            )],
            'postgres' => ['sudo -u postgres psql -lqt | cut -d \| -f1 | grep -qw '.escapeshellarg($database->name).' && echo "database present"'],
            'mongodb' => [sprintf(
                'mongosh %s --quiet --eval %s',
                $this->mongoAuthFlags(),
                escapeshellarg('printjson(db.getSiblingDB('.json_encode($database->name).').getCollectionNames())')
            )],
            default => [],
        };
    }

    /**
     * @return array<int, string>
     */
    protected function dropCommands(Database $database): array
    {
        $name = $database->name;
        $user = $database->username;

        return match ($database->engine) {
            'mysql' => [
                sprintf('sudo mysql -e %s', escapeshellarg(sprintf('DROP DATABASE IF EXISTS `%s`;', $name))),
                sprintf('sudo mysql -e %s', escapeshellarg(sprintf("DROP USER IF EXISTS '%s'@'localhost'; FLUSH PRIVILEGES;", $user))),
            ],

            'postgres' => [
                sprintf('sudo -u postgres psql -c %s', escapeshellarg(sprintf('DROP DATABASE IF EXISTS "%s";', $name))),
                sprintf('sudo -u postgres psql -c %s', escapeshellarg(sprintf('DROP ROLE IF EXISTS "%s";', $user))),
            ],

            'mongodb' => [
                sprintf(
                    'mongosh %s --quiet --eval %s',
                    $this->mongoAuthFlags(),
                    escapeshellarg(sprintf(
                        'db = db.getSiblingDB(%s); if (db.getUser(%s)) { db.dropUser(%2$s); } db.dropDatabase();',
                        json_encode($name),
                        json_encode($user)
                    ))
                ),
            ],

            default => [],
        };
    }

    protected function mongoAuthFlags(): string
    {
        $password = $this->settings->secret('mongo_root_password');

        if (blank($password)) {
            return '';
        }

        return sprintf(
            '-u panelAdmin -p %s --authenticationDatabase admin',
            escapeshellarg((string) $password)
        );
    }
}

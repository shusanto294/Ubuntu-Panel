<?php

namespace Tests\Feature;

use App\Models\Site;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\Concerns\InstallsServices;
use Tests\TestCase;

/**
 * Every prop a page component asks for is a prop its controller sends.
 *
 * A controller that sends `availableEngines` to a component declaring
 * `engineOptions` is not an error anywhere: Inertia hands over what it was
 * given, Vue defaults the missing prop to undefined, and the page renders
 * perfectly while showing nothing. The Databases page shipped that way and
 * three rounds of debugging went looking at everything except the two names.
 *
 * Controller tests cannot catch it — they assert on what was sent, which is
 * the half that was right.
 */
class PagePropsTest extends TestCase
{
    use InstallsServices, RefreshDatabase;

    /**
     * The props a component declares, and whether each has a default.
     *
     * @return array<string, bool> name => has a default
     */
    protected function declaredProps(string $component): array
    {
        $path = resource_path('js/Pages/'.$component.'.vue');

        $this->assertFileExists($path, "no component for {$component}");

        $source = file_get_contents($path);

        if (! preg_match('/defineProps\(\{(.*?)\n\}\)/s', $source, $match)) {
            return [];
        }

        $props = [];

        // `name: Type,` or `name: { type: X, default: ... },` at one level in.
        foreach (preg_split('/\r?\n/', $match[1]) as $line) {
            if (preg_match('/^\s{4}([A-Za-z_][A-Za-z0-9_]*)\s*:\s*(.*)$/', $line, $prop)) {
                $props[$prop[1]] = str_contains($prop[2], 'default');
            }
        }

        return $props;
    }

    public static function pages(): array
    {
        return [
            'dashboard' => ['dashboard', [], 'System/Overview'],
            'settings' => ['settings', [], 'System/Settings'],
            'sites index' => ['sites.index', [], 'Sites/Index'],
            'sites create' => ['sites.create', [], 'Sites/Create'],
            'databases' => ['databases.index', [], 'Databases/Index'],
            'email' => ['email.index', [], 'Email/Index'],
            'profile' => ['profile.edit', [], 'Profile/Edit'],
        ];
    }

    public function test_a_page_is_sent_every_prop_it_asks_for(): void
    {
        $this->markInstalled(['base', 'nginx', 'php', 'mysql', 'wpcli', 'node']);

        $user = User::factory()->create();

        foreach (self::pages() as [$route, $params, $component]) {
            $sent = array_keys(
                $this->actingAs($user)->get(route($route, $params))->viewData('page')['props']
            );

            foreach ($this->declaredProps($component) as $prop => $hasDefault) {
                if ($hasDefault) {
                    // Optional by construction: the component says what to do
                    // without it.
                    continue;
                }

                $this->assertContains(
                    $prop,
                    $sent,
                    "{$component} declares `{$prop}` but {$route} does not send it — ".
                    'the page will render with it undefined and quietly show nothing'
                );
            }
        }
    }

    public function test_the_site_page_is_sent_every_prop_it_asks_for(): void
    {
        $user = User::factory()->create();

        $site = Site::create([
            'user_id' => $user->id,
            'domain' => 'app.example.com',
            'root_path' => '/var/www/app.example.com',
            'php_version' => '8.3',
        ]);

        $sent = array_keys(
            $this->actingAs($user)->get(route('sites.show', $site))->viewData('page')['props']
        );

        foreach ($this->declaredProps('Sites/Show') as $prop => $hasDefault) {
            if (! $hasDefault) {
                $this->assertContains($prop, $sent, "Sites/Show declares `{$prop}` unsent");
            }
        }
    }
}

<?php

namespace Tests\Feature;

use RecursiveDirectoryIterator;
use RecursiveIteratorIterator;
use Tests\TestCase;

/**
 * Every class this codebase names actually exists.
 *
 * `app(\App\Services\Cloudflare\CloudflareDnsManager::class)` is a string as
 * far as PHP is concerned. Renaming the class does not break it, the linter
 * does not see it, `php -l` is happy, and nothing fails until the line runs —
 * which for a step buried in a deployment recipe means a site that installs
 * WordPress, writes its vhost, reloads nginx, and only then falls over.
 *
 * That is exactly what happened when Cloudflare stopped being the only DNS
 * provider: one reference in one file was excluded from the rename by hand and
 * nothing noticed for four releases.
 */
class ClassReferencesTest extends TestCase
{
    public function test_every_class_named_in_the_source_exists(): void
    {
        $missing = [];

        foreach ($this->sourceFiles() as $file) {
            $source = file_get_contents($file);

            // `Foo\Bar::class`, with or without a leading backslash.
            preg_match_all('/\\\\?(App(?:\\\\[A-Za-z_][A-Za-z0-9_]*)+)::class/', $source, $matches);

            foreach (array_unique($matches[1]) as $class) {
                if ($this->resolves($class)) {
                    continue;
                }

                $missing[] = basename($file).' → '.$class;
            }
        }

        $this->assertSame([], $missing, "These classes are named but do not exist:\n".implode("\n", $missing));
    }

    /** The same for `use` statements, which are equally silent when wrong. */
    public function test_every_imported_class_exists(): void
    {
        $missing = [];

        foreach ($this->sourceFiles() as $file) {
            $source = file_get_contents($file);

            preg_match_all('/^use\s+(App\\\\[^\s;]+);/m', $source, $matches);

            foreach (array_unique($matches[1]) as $class) {
                if ($this->resolves($class)) {
                    continue;
                }

                $missing[] = basename($file).' → '.$class;
            }
        }

        $this->assertSame([], $missing, "These imports point at nothing:\n".implode("\n", $missing));
    }

    /**
     * Does this name resolve to something?
     *
     * A stale composer classmap can still hold an entry for a file that has
     * been deleted, and the autoloader then raises rather than answering. That
     * is a missing class too — and reported as one, instead of as a warning
     * about a stream that could not be opened.
     */
    protected function resolves(string $class): bool
    {
        try {
            return @class_exists($class)
                || @interface_exists($class)
                || @trait_exists($class)
                || @enum_exists($class);
        } catch (\Throwable $e) {
            return false;
        }
    }

    /**
     * @return array<int, string>
     */
    protected function sourceFiles(): array
    {
        $files = [];

        $iterator = new RecursiveIteratorIterator(
            new RecursiveDirectoryIterator(app_path(), RecursiveDirectoryIterator::SKIP_DOTS)
        );

        foreach ($iterator as $file) {
            if ($file->isFile() && $file->getExtension() === 'php') {
                $files[] = $file->getPathname();
            }
        }

        $this->assertNotEmpty($files, 'found no source to check');

        return $files;
    }
}

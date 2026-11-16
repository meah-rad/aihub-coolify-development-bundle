<?php

namespace Database\Seeders;

use App\Models\PrivateKey;
use Illuminate\Database\Seeder;

class PrivateKeySeeder extends Seeder
{
    /**
     * Run the database seeds.
     */
    public function run(): void
    {
        PrivateKey::create([
            'uuid' => 'ssh',
            'team_id' => 0,
            'name' => 'Testing Host Key',
            'description' => 'This is a test docker container',
            'private_key' => 'REDACTED_TEST_SSH_PRIVATE_KEY',
        ]);

        $githubAppKey = env('COOLIFY_DEVELOPMENT_GITHUB_APP_PRIVATE_KEY');
        if (is_string($githubAppKey) && $githubAppKey !== '') {
            PrivateKey::create([
                'uuid' => 'github-key',
                'team_id' => 0,
                'name' => 'development-github-app',
                'description' => 'This is the key for using the development GitHub app',
                'private_key' => $githubAppKey,
                'is_git_related' => true,
            ]);
        }
    }
}

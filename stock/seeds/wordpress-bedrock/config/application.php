<?php
// Deployment configuration comes from environment bindings, never a committed .env.
function required_env(string $name): string {
    $value = getenv($name);
    if ($value === false || $value === '') {
        throw new RuntimeException("Missing required setting: $name");
    }
    return $value;
}
// The site's address. A bound WP_HOME is the canonical address. Otherwise it
// is this deployment's own address: AppTown's front door routes a request
// here by its Host and passes that Host on, and APPTOWN_ORIGINS lists the
// origins the app is served at, so a Host matching one of them (exactly, or
// under a *. wildcard) is where the visitor reached this deployment; that
// origin gives the scheme. Any other Host is refused, as Django and Rails
// refuse a host they were not told. WP-CLI (init, maintenance) has no
// request: WordPress reads WP_HOME on every web request, so the placeholder
// only fills the home option init's install writes.
function deployment_home(): string {
    $bound = getenv('WP_HOME');
    if ($bound !== false && $bound !== '') {
        return $bound;
    }
    if (PHP_SAPI === 'cli') {
        return 'http://localhost';
    }
    $origins = array_filter(array_map('trim', explode(',', getenv('APPTOWN_ORIGINS') ?: '')));
    if (!$origins) {
        return required_env('WP_HOME');
    }
    $host = strtolower($_SERVER['HTTP_HOST'] ?? '');
    if (preg_match('/^([a-z0-9-]+(?:\.[a-z0-9-]+)*)(:[0-9]{1,5})?$/', $host, $m)) {
        foreach ($origins as $origin) {
            $scheme = parse_url($origin, PHP_URL_SCHEME);
            $pattern = strtolower((string) parse_url($origin, PHP_URL_HOST));
            if (!in_array($scheme, ['http', 'https'], true) || $pattern === '') {
                continue;
            }
            $matches = str_starts_with($pattern, '*.')
                ? str_ends_with($m[1], substr($pattern, 1))
                : $m[1] === $pattern;
            if ($matches) {
                return $scheme . '://' . $host;
            }
        }
    }
    http_response_code(400);
    header('Content-Type: text/plain');
    echo "This site is not served at this address.\n";
    exit;
}
$home = rtrim(deployment_home(), '/');
if (!filter_var($home, FILTER_VALIDATE_URL) || !in_array(parse_url($home, PHP_URL_SCHEME), ['http', 'https'], true)) {
    throw new RuntimeException('WP_HOME must be one explicit http(s) origin');
}
define('WP_HOME', $home);
define('WP_SITEURL', $home . '/wp');
$environment = getenv('WP_ENVIRONMENT_TYPE') ?: 'production';
if (!in_array($environment, ['local', 'development', 'staging', 'production'], true)) {
    throw new RuntimeException('Invalid WP_ENVIRONMENT_TYPE');
}
define('WP_ENVIRONMENT_TYPE', $environment);
define('DB_NAME', required_env('DB_NAME'));
define('DB_USER', required_env('DB_USER'));
define('DB_PASSWORD', required_env('DB_PASSWORD'));
define('DB_HOST', required_env('DB_HOST') . ':' . (getenv('DB_PORT') ?: '3306'));
define('DB_CHARSET', 'utf8mb4');
define('DB_COLLATE', '');
$table_prefix = 'wp_';
foreach (['AUTH_KEY', 'SECURE_AUTH_KEY', 'LOGGED_IN_KEY', 'NONCE_KEY', 'AUTH_SALT', 'SECURE_AUTH_SALT', 'LOGGED_IN_SALT', 'NONCE_SALT'] as $key) {
    define($key, required_env($key));
}
define('WP_CONTENT_DIR', dirname(__DIR__) . '/web/app');
define('WP_CONTENT_URL', $home . '/app');
define('DISALLOW_FILE_EDIT', true);
define('DISALLOW_FILE_MODS', true);
define('AUTOMATIC_UPDATER_DISABLED', true);
define('WP_AUTO_UPDATE_CORE', false);
define('WP_DEBUG', false);
define('WP_TEMP_DIR', sys_get_temp_dir());
// Uploads live in the deployment's s3 need, whose variables AppTown
// injects. The store has no public address, so media URLs are the site's
// own (/app/uploads/..., where WordPress keeps uploads on disk) and
// web/media.php serves them from the bucket. No object is made public.
define('S3_UPLOADS_BUCKET', required_env('S3_BUCKET'));
define('S3_UPLOADS_REGION', required_env('AWS_REGION'));
define('S3_UPLOADS_KEY', required_env('AWS_ACCESS_KEY_ID'));
define('S3_UPLOADS_SECRET', required_env('AWS_SECRET_ACCESS_KEY'));
define('S3_UPLOADS_BUCKET_URL', WP_CONTENT_URL);
// The plugin's fallback ACL is public-read; web/app/mu-plugins/s3-compatible.php
// removes every ACL, and this keeps any it misses private.
define('S3_UPLOADS_OBJECT_ACL', 'private');
required_env('AWS_ENDPOINT_URL_S3');
if (!defined('ABSPATH')) { define('ABSPATH', dirname(__DIR__) . '/web/wp/'); }
// Trust only the admitted proxy's forwarded scheme; ingress must overwrite it.
if (($_SERVER['HTTP_X_FORWARDED_PROTO'] ?? '') === 'https') { $_SERVER['HTTPS'] = 'on'; }

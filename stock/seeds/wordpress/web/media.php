<?php
// Serves uploaded media from the deployment's s3 need. The store has no
// public address, so a browser reaches an upload through the site itself:
// Apache sends /app/uploads/<path> here, and this reads the object
// uploads/<path> from the need's bucket with the need's key. WordPress is
// not loaded. Only GET and HEAD of one object under uploads/ are answered;
// nothing a visitor sends reaches the store but the key and a few
// conditional and range headers.
require dirname(__DIR__) . '/vendor/autoload.php';

use Aws\S3\Exception\S3Exception;
use Aws\S3\S3Client;

function refuse(int $status, string $why): never {
    http_response_code($status);
    header('Content-Type: text/plain; charset=utf-8');
    header('X-Content-Type-Options: nosniff');
    header('Cache-Control: no-store');
    if ($_SERVER['REQUEST_METHOD'] !== 'HEAD') {
        echo $why, "\n";
    }
    exit;
}

$method = $_SERVER['REQUEST_METHOD'] ?? '';
if ($method !== 'GET' && $method !== 'HEAD') {
    header('Allow: GET, HEAD');
    refuse(405, 'Media can only be read.');
}
$path = parse_url($_SERVER['REQUEST_URI'] ?? '', PHP_URL_PATH);
$prefix = '/app/uploads/';
if (!is_string($path) || !str_starts_with($path, $prefix)) {
    refuse(404, 'Not found.');
}
$rest = rawurldecode(substr($path, strlen($prefix)));
$segments = explode('/', $rest);
foreach ($segments as $segment) {
    if ($segment === '' || $segment === '.' || $segment === '..' || preg_match('/[\x00-\x1f\x7f\\\\]/', $segment)) {
        refuse(404, 'Not found.');
    }
}
$key = 'uploads/' . $rest;

$env = static function (string $name): string {
    $value = getenv($name);
    if ($value === false || $value === '') {
        error_log("media: missing setting $name");
        refuse(502, 'Media storage is not configured.');
    }
    return $value;
};
$s3 = new S3Client([
    'version' => '2006-03-01',
    'region' => $env('AWS_REGION'),
    'endpoint' => $env('AWS_ENDPOINT_URL_S3'),
    'use_path_style_endpoint' => true,
    'credentials' => ['key' => $env('AWS_ACCESS_KEY_ID'), 'secret' => $env('AWS_SECRET_ACCESS_KEY')],
    'request_checksum_calculation' => 'when_required',
    'response_checksum_validation' => 'when_required',
    'http' => ['connect_timeout' => 5, 'timeout' => 60],
]);
$request = ['Bucket' => $env('S3_BUCKET'), 'Key' => $key];
if (isset($_SERVER['HTTP_IF_NONE_MATCH'])) {
    $request['IfNoneMatch'] = $_SERVER['HTTP_IF_NONE_MATCH'];
} elseif (isset($_SERVER['HTTP_IF_MODIFIED_SINCE'])) {
    $request['IfModifiedSince'] = $_SERVER['HTTP_IF_MODIFIED_SINCE'];
}
// One byte range, as a browser asks for audio and video; anything else is
// answered with the whole object.
if ($method === 'GET' && preg_match('/^bytes=[0-9]*-[0-9]*$/', $_SERVER['HTTP_RANGE'] ?? '') && $_SERVER['HTTP_RANGE'] !== 'bytes=-') {
    $request['Range'] = $_SERVER['HTTP_RANGE'];
}
try {
    $object = $method === 'HEAD'
        ? $s3->headObject($request)
        : $s3->getObject($request + ['@http' => ['stream' => true]]);
} catch (S3Exception $e) {
    $status = $e->getStatusCode();
    if ($status === 404) {
        refuse(404, 'Not found.');
    }
    if ($status === 416) {
        refuse(416, 'Range not satisfiable.');
    }
    error_log(sprintf('media: reading %s: %s %s', $key, $status ?? 'no answer', $e->getAwsErrorCode() ?? $e->getMessage()));
    refuse(502, 'Media storage did not answer.');
}

// A store's 304 is a result, not an error: the browser's copy is current.
if (($object['@metadata']['statusCode'] ?? 200) === 304) {
    http_response_code(304);
    if (isset($object['ETag'])) {
        header('ETag: ' . $object['ETag']);
    }
    header('Cache-Control: public, max-age=3600');
    exit;
}

// Media shares the site's origin, so only types a browser shows without
// running script are shown inline; everything else (HTML and SVG among
// them) is a download.
$type = (string) ($object['ContentType'] ?? '');
$inline = preg_match('#^(image/(jpeg|png|gif|webp|avif|bmp|tiff|x-icon|vnd\.microsoft\.icon)|audio/[a-z0-9.+-]+|video/[a-z0-9.+-]+|application/pdf)$#i', $type);
http_response_code(isset($object['ContentRange']) ? 206 : 200);
header('Content-Type: ' . ($type !== '' ? $type : 'application/octet-stream'));
header('X-Content-Type-Options: nosniff');
if (!$inline) {
    header('Content-Disposition: attachment');
}
header('Cache-Control: public, max-age=3600');
header('Accept-Ranges: bytes');
foreach (['ContentLength' => 'Content-Length', 'ContentRange' => 'Content-Range', 'ETag' => 'ETag'] as $field => $name) {
    if (isset($object[$field]) && $object[$field] !== '') {
        header($name . ': ' . $object[$field]);
    }
}
if (isset($object['LastModified'])) {
    header('Last-Modified: ' . gmdate('D, d M Y H:i:s', $object['LastModified']->getTimestamp()) . ' GMT');
}
if ($method === 'HEAD') {
    exit;
}
$body = $object['Body'];
while (!$body->eof()) {
    echo $body->read(65536);
    flush();
}

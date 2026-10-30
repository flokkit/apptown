<?php
// The plugin's published extension point keeps endpoint behavior in app code.
add_filter('s3_uploads_s3_client_params', static function (array $params): array {
    $params['endpoint'] = required_env('AWS_ENDPOINT_URL_S3');
    $params['use_path_style_endpoint'] = true;
    // AWS SDK's optional new checksums are not implemented by every S3 store.
    $params['request_checksum_calculation'] = 'when_required';
    $params['response_checksum_validation'] = 'when_required';
    return $params;
});

// Objects carry no ACL: the need's contract has none, and on a cloud store a
// public-read object would be published at the store's own address. The
// plugin sends one on several writes (uploads, folder markers, renames, its
// bulk CLI upload), so it is removed from every command its client sends.
// Media reaches browsers only through web/media.php.
add_action('plugins_loaded', static function (): void {
    if (class_exists('S3_Uploads\\Plugin')) {
        S3_Uploads\Plugin::get_instance()->s3()->getHandlerList()->appendInit(
            Aws\Middleware::mapCommand(static function (Aws\CommandInterface $command): Aws\CommandInterface {
                unset($command['ACL']);
                return $command;
            }),
            'apptown-no-object-acl'
        );
    }
});

// Preview indexing is a request setting, never a persisted production option.
if (WP_ENVIRONMENT_TYPE !== 'production') {
    add_filter('pre_option_blog_public', static fn() => '0');
}

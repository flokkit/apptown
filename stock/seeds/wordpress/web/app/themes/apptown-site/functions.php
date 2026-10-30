<?php
add_action('after_setup_theme', static function (): void {
    add_theme_support('title-tag');
    add_theme_support('post-thumbnails');
    add_theme_support('html5', ['search-form', 'gallery', 'caption', 'style', 'script']);
    register_nav_menus(['primary' => __('Primary navigation', 'apptown-site')]);
});
add_action('wp_enqueue_scripts', static function (): void {
    wp_enqueue_style('apptown-site', get_stylesheet_uri(), [], '1.0.0');
});
function apptown_site_navigation_fallback(): void {
    echo '<ul class="menu"><li><a href="' . esc_url(home_url('/')) . '">Home</a></li>';
    wp_list_categories(['title_li' => '', 'show_count' => false, 'depth' => 1]);
    echo '</ul>';
}

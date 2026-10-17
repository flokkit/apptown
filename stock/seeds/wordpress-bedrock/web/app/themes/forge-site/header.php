<!doctype html>
<html <?php language_attributes(); ?>>
<head><meta charset="<?php bloginfo('charset'); ?>"><meta name="viewport" content="width=device-width,initial-scale=1"><?php wp_head(); ?></head>
<body <?php body_class(); ?>><?php wp_body_open(); ?>
<a class="skip-link screen-reader-text" href="#main">Skip to content</a>
<header class="site-header"><div class="wrap header-row"><div><a class="brand" href="<?php echo esc_url(home_url('/')); ?>"><?php bloginfo('name'); ?></a><div class="tagline"><?php bloginfo('description'); ?></div></div>
<nav aria-label="Primary navigation"><?php wp_nav_menu(['theme_location' => 'primary', 'container' => false, 'fallback_cb' => 'forge_site_navigation_fallback']); ?></nav></div></header>

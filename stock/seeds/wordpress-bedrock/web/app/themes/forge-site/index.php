<?php get_header(); ?>
<main id="main" class="wrap">
<?php if (is_singular()): while (have_posts()): the_post(); ?>
<article class="article"><h1><?php the_title(); ?></h1><p class="meta"><?php echo esc_html(get_the_date()); ?> · <?php the_author(); ?></p>
<?php if (has_post_thumbnail()) { the_post_thumbnail('large', ['class' => 'featured']); } ?>
<div class="content"><?php the_content(); wp_link_pages(); ?></div><?php if (has_category()) { ?><p class="meta"><?php the_category(' · '); ?></p><?php } ?></article>
<?php endwhile; else: ?>
<div class="intro"><h1><?php if (is_search()) { printf(esc_html__('Search: %s', 'forge-site'), esc_html(get_search_query())); } elseif (is_archive()) { the_archive_title(); } else { echo 'Latest stories'; } ?></h1><?php if (is_archive()) { the_archive_description(); } ?></div>
<div class="posts"><?php while (have_posts()): the_post(); ?>
<article class="card"><?php if (has_post_thumbnail()): ?><a href="<?php the_permalink(); ?>" aria-label="<?php echo esc_attr(get_the_title()); ?>"><?php the_post_thumbnail('medium_large'); ?></a><?php endif; ?>
<p class="meta"><?php echo esc_html(get_the_date()); ?> · <?php the_category(' · '); ?></p><h2><a href="<?php the_permalink(); ?>"><?php the_title(); ?></a></h2><?php the_excerpt(); ?></article>
<?php endwhile; ?></div>
<?php if (!have_posts() && $wp_query->post_count === 0): ?><p>No stories found. Try another search.</p><?php get_search_form(); endif; ?>
<?php the_posts_navigation(); endif; ?></main><?php get_footer(); ?>

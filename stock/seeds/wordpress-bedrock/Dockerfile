# Composer resolves only the committed lock; core, plugins and theme ship together.
FROM composer:2.10.3@sha256:af98f42dfff7c68ba8d53c2164fd9fde1087b7d449514baa38c418b1f6bc4bac AS composer
FROM php:8.3.35-apache-bookworm@sha256:b79a53c3fb1dd4b9f350a8d99d84951512b6c21044e61bbecfee8af030b3722e AS runtime
RUN apt-get update && apt-get upgrade -y \
    && apt-get install -y --no-install-recommends libpng-dev libjpeg62-turbo-dev libwebp-dev unzip \
    && docker-php-ext-configure gd --with-jpeg --with-webp \
    && docker-php-ext-install -j2 mysqli gd opcache \
    && rm -rf /var/lib/apt/lists/* \
    && a2enmod rewrite \
    && sed -i 's/Listen 80/Listen 8080/' /etc/apache2/ports.conf \
    && sed -i 's#/var/run/apache2#/tmp/apache2#g;s#/var/lock/apache2#/tmp/apache2-lock#g' /etc/apache2/envvars
COPY --from=composer /usr/bin/composer /usr/local/bin/composer
WORKDIR /app
COPY composer.json composer.lock ./
RUN composer install --no-dev --prefer-dist --no-interaction --no-progress
RUN curl -fsSL https://github.com/wp-cli/wp-cli/releases/download/v2.12.0/wp-cli-2.12.0.phar -o /usr/local/bin/wp-cli.phar \
    && echo 'ce34ddd838f7351d6759068d09793f26755463b4a4610a5a5c0a97b68220d85c  /usr/local/bin/wp-cli.phar' | sha256sum -c -
COPY . .
COPY config/apache.conf /etc/apache2/sites-available/000-default.conf
COPY config/prefork.conf /etc/apache2/mods-available/mpm_prefork.conf
# The image ships no php.ini, so PHP's own default shows errors on the page:
# a fatal error then reaches visitors as text with status 200, and AppTown's
# front-page check sees a page that answered. Hidden and logged, a fatal is a
# 500 the check fails, and the container's log (stderr) says why.
RUN chmod -R a-w /app && chmod a+rx /app/bin/* \
    && printf 'expose_php=Off\ndisplay_errors=Off\nlog_errors=On\nerror_log=/dev/stderr\nupload_max_filesize=16M\npost_max_size=20M\nmemory_limit=96M\n' > /usr/local/etc/php/conf.d/app.ini
ENV APACHE_RUN_DIR=/tmp/apache2 APACHE_LOCK_DIR=/tmp/apache2-lock APACHE_PID_FILE=/tmp/apache2/apache2.pid WP_CLI_CACHE_DIR=/tmp/wp-cli-cache HOME=/tmp
USER www-data
EXPOSE 8080
CMD ["apache2-foreground"]

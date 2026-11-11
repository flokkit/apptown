SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE c.relkind IN ('r','p','f') AND n.nspname <> 'information_schema' AND n.nspname !~ '^pg_'

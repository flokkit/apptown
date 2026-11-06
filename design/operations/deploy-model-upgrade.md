# Upgrading landing and deployment

Before installing a build that removes the workflow `deploy` step, let all
in-flight landings finish or drain them. Admitted runs retain their workflow
definition; the new build does not execute the removed step kind.

Existing `main` deployments, or `prod` where there is no `main`, follow main.
Other landing-fed deployments follow main too. Promotion and schedule feeds
become promotion feeds from a follows-main deployment when one exists, and
manual feeds otherwise. Migration records no setter for follows-main settings.

After installing, sign in as a person with deployment permission on the
projects and run:

```sh
apptown deployment confirm-feed --all
```

Protected deployments also require protected-release permission. The command
confirms only settings the caller may authorize. A single setting can be
confirmed with `apptown deployment confirm-feed <project> <deployment>` or
**Confirm follows main** on its deployment card.

Until confirmed, automatic releases fail with “follows main is not confirmed
for this deployment”. Landings still finish. Confirmation records the person,
credential and settings revision. A subsequent landing requests its bundle
under that setting; confirmation does not replay a failed release.

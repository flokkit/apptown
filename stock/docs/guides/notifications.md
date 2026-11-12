# Notifications

AppTown sends its attention items to channels an administrator sets up under
**Integrations → Notifications**. Each channel receives items from every project.
With no channel, AppTown sends nothing. Email goes only to the channel’s **To**
addresses. Project members are not emailed automatically.

## Add a channel

1. Open **Integrations → Secrets**. Save the webhook address or SMTP password as
   an instance secret. Secret values are write-only; pages show their names.
2. Open **Notifications → Add channel**. Choose a format or Email (SMTP).
3. Choose the secret’s name, enter the channel’s settings and save.
4. Use **Send test**. The result appears in recent deliveries. Open the channel
   row to edit or remove it.

For Slack, use an incoming webhook URL. For Discord, use the channel webhook
URL. For Microsoft Teams, use a Workflows incoming webhook URL that accepts an
Adaptive Card message. For ntfy, use the full topic URL. The JSON format sends
an object with a delivery `id`, `kind`, instance name and item or digest items.
It also supplies `X-AppTown-Delivery` as an HTTP header.

For email, enter the SMTP host, port (587 by default), security, username,
password secret, From address and comma-separated To addresses. STARTTLS is
the default; implicit TLS and unencrypted delivery are also available. Password
authentication requires TLS. Each message has a stable `Message-ID`.

## When notices arrive

Confirmed, current warnings arrive within about two minutes. Advisory items,
unknown results and old readings wait for the daily digest. A resolution notice
follows when a source confirms an announced item has cleared. An unreadable or
deliberately withheld source cannot confirm recovery.

Each channel chooses a digest hour, **0–23 in UTC**, default 8. The digest lists
items still open, items needing attention, results not known and items cleared
since the previous digest. Empty digests are skipped. A digest is also skipped
if AppTown is down for that whole hour.

The first successful evaluation records existing conditions quietly; they
appear in the next digest. A channel added later receives subsequent events
and its next digest.

## Failed deliveries

Recent deliveries show sent, retrying or failed, with attempts and a safe reason.
AppTown retries up to six attempts through its job queue. A channel’s latest
finished delivery failing becomes an attention item; a later successful delivery
clears it. Check the channel’s settings and send a test.

Restarts preserve delivery records. A crash after the destination accepts a
notice but before AppTown records success can send a duplicate. The delivery
ID enables receivers to deduplicate; chat services and email recipients may
still see both copies. Delivery history and cleared items are kept for 30 days.

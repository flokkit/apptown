// AppTown's chat draft (design/subsystems/console.md, "Following a chat"):
// pi records a message only once it ends, so this writes the message pi is
// writing now beside its record, for the chat page to show as it grows,
// and, when pi's turn settles (nothing more will follow: no retry, no
// queued message), a mark saying so, which is how AppTown knows the
// helper has handed back.
// Only the chat's own pi writes it: one whose session file is in the chat's
// sessions folder (a delegate started with the same home has its own
// session elsewhere). The draft names that file; AppTown shows it only for
// the record it projects, and only until the record holds the message,
// which shares the draft's timestamp. It is written aside and renamed into
// place, at most every 50 ms.
const fs = require('node:fs');
const path = require('node:path');

const sessions = __SESSIONS__;
const file = path.join(sessions, 'draft.json');

module.exports = function (pi) {
  let pending = null;
  let timer = null;
  let last = 0;
  const write = () => {
    timer = null;
    const draft = pending;
    pending = null;
    last = Date.now();
    try {
      fs.writeFileSync(file + '.tmp', JSON.stringify(draft));
      fs.renameSync(file + '.tmp', file);
    } catch {
      // The draft is a view; the record still gets the message.
    }
  };
  // The chat's own record file, or '' for any other pi.
  const own = ctx => {
    const file = ctx.sessionManager.getSessionFile();
    return file && path.dirname(file) === sessions ? path.basename(file) : '';
  };
  pi.on('message_update', (event, ctx) => {
    const message = event.message;
    if (!message || message.role !== 'assistant') return;
    const record = own(ctx);
    if (!record) return;
    pending = {
      type: 'draft',
      record,
      message: {
        role: 'assistant',
        timestamp: message.timestamp,
        content: (message.content || []).filter(part => part.type === 'text' || part.type === 'toolCall'),
      },
    };
    if (!timer) timer = setTimeout(write, Math.max(0, 50 - (Date.now() - last)));
  });
  // pi has recorded the turn's every message by now; a draft still waiting
  // to be written is dropped so it cannot land after the mark.
  pi.on('agent_settled', (event, ctx) => {
    const record = own(ctx);
    if (!record) return;
    if (timer) clearTimeout(timer);
    pending = {type: 'settled', record, at: Date.now()};
    write();
  });
};

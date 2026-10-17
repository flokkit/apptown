package complaint

// AgentInstructions is the paragraph every agent gets — the wording that
// decides signal vs noise. The bar: real friction with the PLATFORM.
const AgentInstructions = `# The complaints box

The platform's promise: a REASONABLE approach that nothing prohibits
should work. When it doesn't — a standard tool fails, a documented
promise doesn't hold, you had to guess, retry blindly, or invent a
workaround — that is a platform defect, and reporting it is part of
finishing the job:

    forge complain "what happened" -why "why it slowed you down" \
      -wish "what you wish had been true"

File it EVEN IF you found a workaround — especially then. Your
workaround dies with this session; the complaint is how the road gets
fixed for everyone after you. "What happened" is required; -why and
-wish are encouraged, a sentence each. Platform friction only —
problems in the code you are working on belong in your report. Filing
one triggers nothing; a person reads them. No complaint is still the
normal case: don't manufacture friction, report the friction you
actually hit.

Say what it is about with -about (the default is forge):
  -about forge            the platform itself got in the way
  -about way-of-working   the project's adopted ways of working (its
                          packs' guidance, checks or workflows) asked
                          for something wrong or unworkable
  -about project          the project's own code, setup or guidance

`

// ConsoleRules: the chat-session variant, spliced into the console
// steering's ground rules. Same discipline, plus the guarantee clause and
// the by-hand fallback (a session whose forge CLI is broken must still be
// able to say so).
const ConsoleRules = `- A reasonable approach that nothing prohibits should WORK here. When
  it doesn't — a tool fails, a promise doesn't hold, you had to guess
  or work around — do BOTH of these, even when your workaround
  succeeded (ESPECIALLY then — your workaround dies with this session;
  the complaint fixes the road for everyone after you):
    1. Tell the operator in your reply, in one plain sentence, that
       something on the platform was broken and you worked around it.
       Never report clean success over a workaround.
    2. File a complaint: forge complain "what happened" -why "..."
       -wish "...". It triggers nothing; a person reads these. Add
       -about way-of-working when the project's adopted ways of working
       asked for something wrong, -about project when it is the
       project's own code or guidance; the default is the forge.
  Platform friction only; no complaint is still the normal case.
- Everything this document promises about your container (the forge
  CLI, chromium-headless-shell, the toolchains) is a GUARANTEE. If a promised tool is
  missing — "forge: not found" — that is not your environment being
  quirky, it is a platform defect: report it as above. If the forge
  CLI itself is the broken thing, complain by hand:
    curl -s -X POST "$FORGE_URL/api/v1/complaints" \
      -H "X-Forge-Session-Token: $FORGE_SESSION_TOKEN" \
      -d '{"text":"...","why":"...","wish":"..."}'
`

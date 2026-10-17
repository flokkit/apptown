// Compile-time checks that the hand-written types which are more precise than
// the generated ones (unions of state words, steps discriminated by type) still
// describe what the server sends: every field they declare must exist on the
// generated type of the same response. A field the server dropped or renamed
// fails the typecheck here, naming the field.
import type * as A from './api'
import type { Activity } from './pages/Dashboard'
import type { Bubble } from './pages/console/Bubbles'
import type { StartedChange } from './pages/console/ChangeBar'
import type { RunRow, ChangeRow } from './parts/runActivity'
import type { RuntimeObservation } from './runtime'
import type * as W from './wire.gen'

type Extra<Hand, Wire> = Exclude<keyof Hand, keyof Wire>
type Declares<Hand, Wire> = [Extra<Hand, Wire>] extends [never] ? true : { serverNeverSends: Extra<Hand, Wire> }
type Item<T> = NonNullable<T> extends readonly (infer E)[] ? E : never

export const handTypesMatchTheServer: [
  Declares<A.ChangeView, W.ChangeCockpit>,
  Declares<A.Change, W.Change>,
  Declares<A.RunDetail, W.RunSnapshot>,
  Declares<A.Run, W.Run>,
  Declares<A.Step, W.Step>,
  Declares<A.RunEvent, W.Event>,
  Declares<A.Artifact, W.Artifact>,
  Declares<A.RunLite, W.ChangeRunSummary>,
  Declares<A.AdrLite, W.ADRChangeSummary>,
  Declares<A.ChangeEvidence, W.CockpitEvidence>,
  Declares<Item<A.ChangeEvidence['targets']>, W.AcceptanceCheck>,
  Declares<Item<A.ChangeEvidence['releases']>, W.ChangeRelease>,
  Declares<A.Dashboard, W.DashboardView>,
  Declares<Item<A.Dashboard['pendingApprovals']>, W.PendingApproval>,
  Declares<Item<A.Dashboard['activeRuns']>, W.ActiveRun>,
  Declares<A.Adr, W.ADRRecord>,
  Declares<Activity, W.InstanceActivity>,
  Declares<Item<Activity['agents']>, W.ActiveAgent>,
  Declares<RunRow, W.RunRow>,
  Declares<ChangeRow, W.ChangeHistory>,
  Declares<RuntimeObservation, W.ActivityDeployment>,
  Declares<Bubble, W.Message>,
  Declares<StartedChange, W.ConsoleChange>,
] = [true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true]

import type { SlackApiClient } from "./client.ts";
import { asArray, getNumber, getString, isRecord } from "../lib/object-type-guards.ts";

export type OrgChartPerson = {
  user_id: string;
  report_count: number;
};

export type OrgChartManager = OrgChartPerson & { peers?: OrgChartPerson[] };

export type OrgChart = {
  user: OrgChartPerson;
  manager_chain: OrgChartManager[];
  peers: OrgChartPerson[];
  direct_reports: OrgChartPerson[];
};

// users.profile.relationships.getOrgChart is an undocumented browser-client endpoint
// (the org chart on a profile). It returns `levels` bottom-up: levels[0] has no focus
// and lists the user's direct reports; each following level's focus is the user, then
// their manager, and so on to the top, with `peers` being everyone under the same manager.
export async function fetchOrgChart(
  client: SlackApiClient,
  options: { userId: string; allPeers?: boolean },
): Promise<OrgChart> {
  const resp = await client.api("users.profile.relationships.getOrgChart", {
    user: options.userId,
  });
  const levels = asArray(resp.levels)
    .filter(isRecord)
    .map((level) => ({
      focus: isRecord(level.focus) ? toPerson(level.focus) : undefined,
      peers: asArray(level.peers)
        .filter(isRecord)
        .map(toPerson)
        .filter((p): p is OrgChartPerson => p !== undefined),
    }));

  const directReports = levels[0] && !levels[0].focus ? levels[0].peers : [];
  const chain = levels.filter((level) => level.focus);
  const [self] = chain;
  if (!self?.focus) {
    throw new Error(`No org chart for user ${options.userId}`);
  }

  // A level's peers are the focus's siblings; the next level up holds their shared manager.
  return {
    user: self.focus,
    manager_chain: chain.slice(1).map((level) => {
      const manager = level.focus as OrgChartPerson;
      if (!options.allPeers) {
        return manager;
      }
      return { ...manager, peers: excluding(level.peers, manager.user_id) };
    }),
    peers: excluding(self.peers, self.focus.user_id),
    direct_reports: directReports,
  };
}

export function collectOrgChartUserIds(chart: OrgChart): string[] {
  return [
    chart.user,
    ...chart.manager_chain.flatMap((m) => [m, ...(m.peers ?? [])]),
    ...chart.peers,
    ...chart.direct_reports,
  ].map((p) => p.user_id);
}

function excluding(people: OrgChartPerson[], userId: string): OrgChartPerson[] {
  return people.filter((p) => p.user_id !== userId);
}

function toPerson(raw: Record<string, unknown>): OrgChartPerson | undefined {
  const userId = getString(raw.user_id);
  return userId ? { user_id: userId, report_count: getNumber(raw.report_count) ?? 0 } : undefined;
}

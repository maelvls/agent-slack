import { describe, expect, test } from "bun:test";
import { collectOrgChartUserIds, fetchOrgChart } from "../src/slack/org-chart.ts";
import type { SlackApiClient } from "../src/slack/client.ts";

const p = (user_id: string, report_count = 0) => ({ user_id, report_count });

// Shape returned by users.profile.relationships.getOrgChart for a manager (U2) with
// one report (U1), one peer (U3), a manager (M1, who has peer M2), and a top (CEO).
const RESPONSE = {
  ok: true,
  levels: [
    { focus: null, peers: [p("U1")] },
    { focus: p("U2", 1), peers: [p("U2", 1), p("U3", 4)] },
    { focus: p("M1", 2), peers: [p("M1", 2), p("M2", 7)] },
    { focus: p("CEO", 2), peers: [] },
  ],
};

function createClient(response: Record<string, unknown>) {
  const calls: { method: string; params: Record<string, unknown> }[] = [];
  const client = {
    api: async (method: string, params: Record<string, unknown> = {}) => {
      calls.push({ method, params });
      return response;
    },
  } as unknown as SlackApiClient;
  return { client, calls };
}

describe("fetchOrgChart", () => {
  test("splits levels into user, manager chain, peers, and direct reports", async () => {
    const { client, calls } = createClient(RESPONSE);

    const chart = await fetchOrgChart(client, { userId: "U2" });

    expect(calls).toEqual([
      { method: "users.profile.relationships.getOrgChart", params: { user: "U2" } },
    ]);
    expect(chart).toEqual({
      user: p("U2", 1),
      manager_chain: [p("M1", 2), p("CEO", 2)],
      peers: [p("U3", 4)],
      direct_reports: [p("U1")],
    });
  });

  test("attaches each manager's peers with --all-peers", async () => {
    const { client } = createClient(RESPONSE);

    const chart = await fetchOrgChart(client, { userId: "U2", allPeers: true });

    expect(chart.manager_chain).toEqual([
      { ...p("M1", 2), peers: [p("M2", 7)] },
      { ...p("CEO", 2), peers: [] },
    ]);
  });

  test("handles a user with no reports", async () => {
    const { client } = createClient({
      ok: true,
      levels: [
        { focus: null, peers: [] },
        { focus: p("U1"), peers: [p("U1")] },
        { focus: p("U2", 1), peers: [] },
      ],
    });

    const chart = await fetchOrgChart(client, { userId: "U1" });

    expect(chart.direct_reports).toEqual([]);
    expect(chart.peers).toEqual([]);
    expect(chart.manager_chain).toEqual([p("U2", 1)]);
  });

  test("throws when the response has no focus user", async () => {
    const { client } = createClient({ ok: true, levels: [] });

    await expect(fetchOrgChart(client, { userId: "U9" })).rejects.toThrow("No org chart");
  });
});

describe("collectOrgChartUserIds", () => {
  test("includes everyone shown, including managers' peers", async () => {
    const { client } = createClient(RESPONSE);
    const chart = await fetchOrgChart(client, { userId: "U2", allPeers: true });

    expect(collectOrgChartUserIds(chart).sort()).toEqual(["CEO", "M1", "M2", "U1", "U2", "U3"]);
  });
});

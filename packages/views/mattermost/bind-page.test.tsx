import { StrictMode } from "react";
import { beforeEach, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../locales/en/common.json";

const auth = vi.hoisted(() => ({ user: { id: "u1", email: "alice@example.com", name: "Alice" } as { id: string; email: string; name: string } | null, isLoading: false }));
const apiMocks = vi.hoisted(() => ({ preview: vi.fn(), redeem: vi.fn(), push: vi.fn() }));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: Object.assign((selector: (state: typeof auth) => unknown) => selector(auth), { getState: () => auth }),
}));
vi.mock("@multica/core/api", () => ({ api: {
  previewMattermostBindingToken: apiMocks.preview,
  redeemMattermostBindingToken: apiMocks.redeem,
} }));
vi.mock("../navigation/context", () => ({
  useNavigation: () => ({ push: apiMocks.push }),
  useOptionalNavigation: () => ({ push: apiMocks.push }),
}));

import { MattermostBindPage } from "./bind-page";

const identity = { workspaceId: "w1", workspaceName: "Acme", installationId: "i1", mattermostUserId: "mm1", username: "alice", serverUrl: "https://chat.example.com" };
function renderPage(token: string | null = "bearer-link") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <StrictMode>
      <QueryClientProvider client={client}>
        <I18nProvider locale="en" resources={{ en: { common: enCommon } }}>
          <MattermostBindPage token={token} />
        </I18nProvider>
      </QueryClientProvider>
    </StrictMode>,
  );
}

beforeEach(() => {
  auth.user = { id: "u1", email: "alice@example.com", name: "Alice" };
  auth.isLoading = false;
  vi.clearAllMocks();
  apiMocks.preview.mockResolvedValue(identity);
  apiMocks.redeem.mockResolvedValue({ workspace_id: "w1", installation_id: "i1", mattermost_user_id: "mm1" });
});

it("never links on navigation and shows the verified identity before consent", async () => {
  renderPage();
  await screen.findByRole("button", { name: "Link account" });
  expect(screen.getByText(/@alice on https:\/\/chat.example.com/)).toHaveTextContent("alice@example.com in workspace Acme");
  expect(apiMocks.redeem).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(apiMocks.push).toHaveBeenCalledWith("/");
  expect(apiMocks.redeem).not.toHaveBeenCalled();
});

it("links exactly once after the explicit action under StrictMode", async () => {
  renderPage();
  fireEvent.click(await screen.findByRole("button", { name: "Link account" }));
  await screen.findByText("You're linked.");
  expect(apiMocks.redeem).toHaveBeenCalledTimes(1);
  expect(apiMocks.redeem).toHaveBeenCalledWith("bearer-link");
});

it("requires login without previewing or consuming a bearer token", () => {
  auth.user = null;
  renderPage();
  fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
  expect(apiMocks.push).toHaveBeenCalledWith("/login?next=%2Fmattermost%2Fbind%3Ftoken%3Dbearer-link");
  expect(apiMocks.preview).not.toHaveBeenCalled();
  expect(apiMocks.redeem).not.toHaveBeenCalled();
});

it("does not offer consent when the server cannot prove the identity", async () => {
  apiMocks.preview.mockResolvedValue({});
  renderPage();
  await screen.findByRole("alert");
  expect(screen.queryByRole("button", { name: "Link account" })).toBeNull();
  expect(apiMocks.redeem).not.toHaveBeenCalled();
});

it("reports an expired token without linking", async () => {
  apiMocks.preview.mockRejectedValue(new Error("410 binding token expired"));
  renderPage();
  expect(await screen.findByRole("alert")).toHaveTextContent(/invalid or expired/);
  expect(apiMocks.redeem).not.toHaveBeenCalled();
});

it("keeps a failed confirmation visible and offers a deliberate retry", async () => {
  apiMocks.redeem.mockRejectedValueOnce(new Error("network failed"));
  renderPage();
  fireEvent.click(await screen.findByRole("button", { name: "Link account" }));
  await screen.findByRole("alert");
  await waitFor(() => expect(screen.getByRole("button", { name: "Link account" })).toBeEnabled());
  expect(apiMocks.redeem).toHaveBeenCalledTimes(1);
});

import type { QueryClient } from "@tanstack/react-query";

export function invalidateControlPlane(queryClient: QueryClient) {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: ["metrics"] }),
    queryClient.invalidateQueries({ queryKey: ["tasks"] }),
    queryClient.invalidateQueries({ queryKey: ["workers"] }),
    queryClient.invalidateQueries({ queryKey: ["repositories"] }),
		queryClient.invalidateQueries({ queryKey: ["definitions"] }),
    queryClient.invalidateQueries({ queryKey: ["workflows"] }),
    queryClient.invalidateQueries({ queryKey: ["automations"] }),
  ]);
}

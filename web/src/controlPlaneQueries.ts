import type { QueryClient } from "@tanstack/react-query";

export function invalidateControlPlane(queryClient: QueryClient) {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: ["metrics"] }),
    queryClient.invalidateQueries({ queryKey: ["tasks"] }),
    queryClient.invalidateQueries({ queryKey: ["workers"] }),
    queryClient.invalidateQueries({ queryKey: ["repositories"] }),
  ]);
}

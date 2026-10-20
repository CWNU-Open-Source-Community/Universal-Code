import { useLocale } from "../lib/locale";
import { useQuery } from "@tanstack/react-query";
import type { APIClient } from "../api/client";
import type { V2PendingModelRoute } from "./components/model-route-control";

export function useV2ImageCapability(client: APIClient, threadID: string,
  pendingRoute: V2PendingModelRoute | null | undefined, enabled: boolean, runActive = false) {
  const { t } = useLocale();
  const route = useQuery({ queryKey: ["v2", "thread", threadID, "model-route"],
    queryFn: ({ signal }) => client.threadModelRoute(threadID, signal),
    enabled: enabled && !!threadID && client.hasModelControl, staleTime: 15_000 });
  const catalog = useQuery({ queryKey: ["v2", "models", "available-routes"],
    queryFn: ({ signal }) => client.availableModelRoutes(signal),
    enabled: enabled && !threadID && client.hasModelControl, staleTime: 30_000 });
  const selectable = catalog.data?.routes.filter((item) => item.selectable) ?? [];
  // Match Thread creation's default/fallback choice, while preserving an
  // explicit draft selection even when it is missing or no longer eligible.
  const model = threadID ? route.data : pendingRoute
    ? catalog.data?.routes.find((item) => item.provider_id === pendingRoute.provider && item.model === pendingRoute.model)
    : selectable.find((item) => item.default_for_routes.includes("code")) ?? selectable[0];
  const capability = model?.vision_capability;
  const state = capability?.state ?? "unknown";
  const loading = threadID ? route.isFetching : catalog.isFetching;
  const failed = threadID ? route.isError : catalog.isError;
  const pendingSwitch = !!threadID && runActive && route.data?.active_run_unchanged === true;
  const hint = loading ? t("正在检查当前模型的图片能力…", "Checking the current model's image capability…") : failed ? t("暂时无法读取模型的图片能力；图片和草稿会保留。", "Could not read the model's image capability. Images and draft are kept.")
    : pendingSwitch ? t("所选模型尚未应用到当前执行。请等本轮结束后再发送图片；图片与草稿会保留。", "The selected model has not been applied to this run. Send images after this turn; images and draft are kept.")
    : state === "unsupported" ? t("当前模型不支持图片。请切换模型，或移除图片后发送。", "The current model does not support images. Switch models or remove the images before sending.")
      : state !== "supported" ? t("当前模型尚未声明图片能力。请在模型设置中确认，或切换到支持图片的模型。", "Image support has not been declared for this model. Confirm it in model settings or choose a model that supports images.")
        : capability?.source === "operator_declared" ? t("将发送原始图片；图片能力来自你的模型配置，尚未做视觉理解验证。", "Original images will be sent. Image support is declared in your model configuration and has not been tested for visual understanding.")
          : t("当前模型支持图片，将随消息发送原始图像。", "The current model supports images. Original images will be sent with the message.");
  return { allowed: state === "supported" && !failed && !loading && !pendingSwitch, hint };
}

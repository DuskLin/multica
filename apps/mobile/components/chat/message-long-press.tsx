/**
 * Cross-platform actions for a chat message bubble. The pressed highlight
 * is cleared on selection or dismissal, including Android's back button.
 */
import { useMobileActionSheet } from "@/lib/use-action-sheet";
import { useCallback, useState } from "react";
import * as Clipboard from "expo-clipboard";
import * as Haptics from "expo-haptics";
import type { ChatMessage } from "@multica/core/types";
import { useChatSelectStore } from "@/data/chat-select-store";
import { useT } from "@/lib/i18n";

export function useChatMessageLongPress(
  message: ChatMessage,
): { onLongPress: () => void; isPressed: boolean } {
  const [isPressed, setIsPressed] = useState(false);
  const { t } = useT("issues");
  const showActionSheet = useMobileActionSheet();

  const onLongPress = useCallback(() => {
    const hasContent = !!message.content;

    Haptics.selectionAsync().catch(() => {});
    setIsPressed(true);

    type Action =
      | { kind: "copy" }
      | { kind: "select" }
      | { kind: "cancel" };

    const options: string[] = [];
    const actions: Action[] = [];
    const push = (label: string, action: Action) => {
      options.push(label);
      actions.push(action);
    };

    if (hasContent) {
      push(t("comments.copy"), { kind: "copy" });
      push(t("comments.select_text"), { kind: "select" });
    }
    push(t("common:actions.cancel"), { kind: "cancel" });

    const cancelButtonIndex = options.length - 1;

    showActionSheet(
      { options, cancelButtonIndex },
      (i) => {
        setIsPressed(false);
        const action = actions[i];
        if (!action || action.kind === "cancel") return;

        switch (action.kind) {
          case "copy":
            if (message.content) {
              Clipboard.setStringAsync(message.content);
              Haptics.notificationAsync(
                Haptics.NotificationFeedbackType.Success,
              ).catch(() => {});
            }
            return;
          case "select":
            useChatSelectStore.getState().setSelecting(message.id);
            return;
        }
      },
    );
  }, [message, t, showActionSheet]);

  return { onLongPress, isPressed };
}

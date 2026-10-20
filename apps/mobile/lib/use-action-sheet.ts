import { useMemo } from "react";
import { useActionSheet } from "@expo/react-native-action-sheet";
import { createActionSheetPresenter } from "@/lib/action-sheet";
import { THEME } from "@/lib/theme";
import { useColorScheme } from "@/lib/use-color-scheme";

export function useMobileActionSheet() {
  const { showActionSheetWithOptions } = useActionSheet();
  const { colorScheme } = useColorScheme();

  return useMemo(
    () => createActionSheetPresenter(showActionSheetWithOptions, {
      colorScheme,
      ...THEME[colorScheme],
    }),
    [showActionSheetWithOptions, colorScheme],
  );
}

import type { ActionSheetOptions, ActionSheetProps } from "@expo/react-native-action-sheet";

export type MobileActionSheetOptions = Pick<
  ActionSheetOptions,
  "options" | "title" | "message" | "destructiveButtonIndex"
> & { cancelButtonIndex: number };

interface Appearance {
  colorScheme: "light" | "dark";
  foreground: string;
  background: string;
  mutedForeground: string;
  destructive: string;
}

export function createActionSheetPresenter(
  show: ActionSheetProps["showActionSheetWithOptions"],
  appearance: Appearance,
) {
  return (
    options: MobileActionSheetOptions,
    onSelect: (index: number) => void | Promise<void>,
  ) => {
    show(
      {
        ...options,
        userInterfaceStyle: appearance.colorScheme,
        tintColor: appearance.foreground,
        containerStyle: { backgroundColor: appearance.background },
        titleTextStyle: { color: appearance.mutedForeground },
        messageTextStyle: { color: appearance.mutedForeground },
        destructiveColor: appearance.destructive,
        // Android sheets must also appear above native modal/formSheet routes.
        useModal: true,
      },
      (index) => onSelect(index ?? options.cancelButtonIndex),
    );
  };
}

export type ShowMobileActionSheet = ReturnType<typeof createActionSheetPresenter>;

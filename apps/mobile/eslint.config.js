// https://docs.expo.dev/guides/using-eslint/
const { defineConfig } = require('eslint/config');
const expoConfig = require("eslint-config-expo/flat");

module.exports = defineConfig([
  expoConfig,
  {
    ignores: ["dist/*"],
  },
  {
    files: ["**/*.{js,jsx,ts,tsx}"],
    rules: {
      "no-restricted-imports": ["error", {
        paths: [{
          name: "react-native",
          importNames: ["ActionSheetIOS"],
          message: "Use useMobileActionSheet from @/lib/use-action-sheet so menus work on Android and iOS.",
        }],
        patterns: [{
          group: ["@react-navigation/*"],
          message: "Import navigation hooks and themes from expo-router to use its navigation context.",
        }],
      }],
      "no-restricted-syntax": ["error",
        {
          selector: "Literal[value=/^sf:/]",
          message: "SF Symbols are iOS-only. Use the existing cross-platform icon library.",
        },
        {
          selector: "TemplateElement[value.raw=/^sf:/]",
          message: "SF Symbols are iOS-only. Use the existing cross-platform icon library.",
        },
      ],
    },
  }
]);

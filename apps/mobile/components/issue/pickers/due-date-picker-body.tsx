/**
 * Native due-date picker body. The caller
 * (a formSheet route) renders the Done / Clear actions in its own header
 * area — this body only handles the picker spinner + the local draft state.
 *
 * due_date is a calendar day (date-only "YYYY-MM-DD", no time/timezone — see
 * @multica/core/issues/date and GH #3618). Mirrors web's
 * packages/views/issues/components/pickers/due-date-picker.tsx: read the stored
 * day into a local-midnight Date for the spinner, write back the picked local
 * day as a date-only string.
 */
import { useState, useEffect, useImperativeHandle, forwardRef } from "react";
import { Platform, View } from "react-native";
import DateTimePicker from "@react-native-community/datetimepicker";
import { toDateOnly, dateOnlyToLocalDate, formatDateOnly } from "@multica/core/issues/date";
import { Button } from "@/components/ui/button";
import { Text } from "@/components/ui/text";
import { useT } from "@/lib/i18n";

interface Props {
  value: string | null;
}

export interface DueDatePickerBodyHandle {
  /** Returns the currently-displayed day as a date-only "YYYY-MM-DD" string. */
  getIso: () => string;
}

function toLocalDay(value: string | null): Date {
  return dateOnlyToLocalDate(value) ?? new Date();
}

export const DueDatePickerBody = forwardRef<DueDatePickerBodyHandle, Props>(
  function DueDatePickerBody({ value }, ref) {
    const [draft, setDraft] = useState<Date>(() => toLocalDay(value));
    const [pickerOpen, setPickerOpen] = useState(true);
    const { t, i18n } = useT("issues");

    useEffect(() => {
      setDraft(toLocalDay(value));
    }, [value]);

    useImperativeHandle(ref, () => ({
      getIso: () => toDateOnly(draft),
    }));

    if (Platform.OS === "android") {
      return (
        <View className="flex-1 items-center pt-2">
          <Button
            variant="outline"
            accessibilityLabel={t("common:fields.due_date")}
            onPress={() => setPickerOpen(true)}
          >
            <Text>
              {formatDateOnly(
                toDateOnly(draft),
                { year: "numeric", month: "long", day: "numeric" },
                i18n.resolvedLanguage ?? i18n.language,
              )}
            </Text>
          </Button>
          {pickerOpen ? (
            <DateTimePicker
              value={draft}
              mode="date"
              display="default"
              onValueChange={(_event, selected) => {
                setPickerOpen(false);
                setDraft(selected);
              }}
              onDismiss={() => setPickerOpen(false)}
            />
          ) : null}
        </View>
      );
    }

    return (
      <View className="flex-1 items-center pt-2">
        <DateTimePicker
          value={draft}
          mode="date"
          display="inline"
          locale={i18n.resolvedLanguage ?? i18n.language}
          onValueChange={(_event, selected) => setDraft(selected)}
        />
      </View>
    );
  },
);

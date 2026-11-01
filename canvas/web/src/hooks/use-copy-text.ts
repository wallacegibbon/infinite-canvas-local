import copy from "copy-to-clipboard";
import { useTranslation } from "react-i18next";

import { toast } from "@/components/kit/toast";

export function useCopyText() {
    const { t } = useTranslation();

    return (value: string, successText = t("common.copied")) => {
        copy(value);
        toast.success(successText);
    };
}

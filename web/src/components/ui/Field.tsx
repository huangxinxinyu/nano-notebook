import * as Label from "@radix-ui/react-label";
import type { InputHTMLAttributes } from "react";
import type { UseFormRegisterReturn } from "react-hook-form";

type FieldProps = Omit<InputHTMLAttributes<HTMLInputElement>, "id"> & {
  id: string;
  label: string;
  registration?: UseFormRegisterReturn;
  error?: string;
};

export function Field({ id, label, registration, error, "aria-describedby": ariaDescribedBy, ...inputProps }: FieldProps) {
  const errorID = `${id}-error`;
  const describedBy = [ariaDescribedBy, error ? errorID : ""].filter(Boolean).join(" ") || undefined;

  return (
    <div className="field">
      <Label.Root htmlFor={id}>{label}</Label.Root>
      <input id={id} aria-invalid={error ? "true" : undefined} aria-describedby={describedBy} {...registration} {...inputProps} />
      {error ? <p id={errorID} className="field-error">{error}</p> : null}
    </div>
  );
}

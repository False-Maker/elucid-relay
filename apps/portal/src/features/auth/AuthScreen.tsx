import { useState, useMemo } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Loader2, Globe2, Mail, Lock, User, KeyRound, ArrowRight, ChevronLeft } from "lucide-react";
import { motion, AnimatePresence } from "framer-motion";
import type { RelayUser, SessionResponse, SessionAudience } from "@elucid-relay/contracts";
import { publicRequest, ApiError } from "@/lib/api-client";
import {
  loginSchema,
  registerSchema,
  resetRequestSchema,
  resetConfirmSchema,
  type LoginValues,
  type RegisterValues,
  type ResetRequestValues,
  type ResetConfirmValues,
} from "@/lib/schemas/auth";
import { cn } from "@/lib/utils";

type AuthMode = "login" | "register" | "reset";

interface AuthScreenProps {
  onAuthed: (user: RelayUser, workspace: Extract<SessionAudience, "portal" | "admin">, token: string) => void;
  onPublic: () => void;
  apiOffline?: boolean;
}

export function AuthScreen({ onAuthed, onPublic, apiOffline }: AuthScreenProps) {
  const [mode, setMode] = useState<AuthMode>("login");

  return (
    <div className="flex min-h-screen items-center justify-center bg-background p-4">
      <motion.div
        initial={{ opacity: 0, y: 12 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.3 }}
        className="w-full max-w-sm"
      >
        {/* Brand */}
        <div className="mb-8 text-center">
          <div className="mx-auto mb-3 flex h-12 w-12 items-center justify-center">
            <svg viewBox="30 60 350 320" width="40" height="40" fill="none">
              <path d="M80 220L124 130L360 78L348 138L142 178Z" fill="#d4ba85" />
              <path d="M80 220L138 209L360 202L354 238L142 240Z" fill="#8294a8" />
              <path d="M80 220L124 310L360 362L348 302L142 262Z" fill="#0f172a" />
              <rect x="77" y="120" width="6" height="200" rx="3" fill="#0f172a" />
              <rect x="78.5" y="216" width="3" height="8" rx="1.5" fill="#8b6b3e" />
            </svg>
          </div>
          <h1 className="text-lg font-semibold">Elucid Relay</h1>
          <p className="mt-0.5 text-sm text-muted">个人 API 中转门户</p>
        </div>

        {/* API offline warning */}
        {apiOffline && (
          <div className="mb-4 rounded-lg border border-destructive/20 bg-destructive/5 px-3 py-2 text-xs text-destructive">
            Gateway API 未连接。请先启动后端，确认 /healthz 返回 ok。
          </div>
        )}

        {/* Card */}
        <div className="rounded-xl border border-border bg-surface p-6 shadow-md">
          <AnimatePresence mode="wait">
            {mode === "login" && (
              <FadePanel key="login">
                <LoginForm
                  onSuccess={onAuthed}
                  onSwitchRegister={() => setMode("register")}
                  onSwitchReset={() => setMode("reset")}
                />
              </FadePanel>
            )}
            {mode === "register" && (
              <FadePanel key="register">
                <RegisterForm
                  onSuccess={onAuthed}
                  onSwitchLogin={() => setMode("login")}
                />
              </FadePanel>
            )}
            {mode === "reset" && (
              <FadePanel key="reset">
                <ResetForm onBack={() => setMode("login")} />
              </FadePanel>
            )}
          </AnimatePresence>
        </div>

        {/* Public link */}
        <button
          onClick={onPublic}
          className="mt-4 flex w-full items-center justify-center gap-1.5 rounded-lg py-2 text-sm text-muted transition-colors hover:text-foreground"
        >
          <Globe2 size={14} />
          查看公开信息
        </button>
      </motion.div>
    </div>
  );
}

/* ─── Login Form ─── */

function LoginForm({
  onSuccess,
  onSwitchRegister,
  onSwitchReset,
}: {
  onSuccess: AuthScreenProps["onAuthed"];
  onSwitchRegister: () => void;
  onSwitchReset: () => void;
}) {
  const [error, setError] = useState("");
  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<LoginValues>({ resolver: zodResolver(loginSchema) });

  async function onSubmit(values: LoginValues) {
    setError("");
    try {
      const data = await publicRequest<SessionResponse>(
        "/api/auth/v1/login",
        { method: "POST", body: JSON.stringify(values) },
      );
      onSuccess(data.user, data.session.audience as "portal" | "admin", data.session.session_token);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "登录失败");
    }
  }

  return (
    <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
      {/* Mode tabs */}
      <div className="flex gap-1 rounded-lg bg-surface-2 p-1">
        <button type="button" className="flex-1 rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground">
          登录
        </button>
        <button type="button" onClick={onSwitchRegister} className="flex-1 rounded-md px-3 py-1.5 text-xs font-medium text-muted hover:text-foreground">
          注册
        </button>
      </div>

      <FieldGroup>
        <InputField
          icon={Mail}
          placeholder="邮箱"
          type="email"
          autoComplete="email"
          error={errors.email?.message}
          {...register("email")}
        />
        <InputField
          icon={Lock}
          placeholder="密码"
          type="password"
          autoComplete="current-password"
          error={errors.password?.message}
          {...register("password")}
        />
      </FieldGroup>

      {error && <ErrorMessage message={error} />}

      <SubmitButton loading={isSubmitting}>登录</SubmitButton>

      <button
        type="button"
        onClick={onSwitchReset}
        className="w-full text-center text-xs text-muted hover:text-bronze transition-colors"
      >
        忘记密码？
      </button>
    </form>
  );
}

/* ─── Register Form ─── */

function RegisterForm({
  onSuccess,
  onSwitchLogin,
}: {
  onSuccess: AuthScreenProps["onAuthed"];
  onSwitchLogin: () => void;
}) {
  const [error, setError] = useState("");
  const [verificationRequired, setVerificationRequired] = useState(false);
  const [verificationSent, setVerificationSent] = useState(false);
  const [sendingCode, setSendingCode] = useState(false);

  const {
    register,
    handleSubmit,
    getValues,
    formState: { errors, isSubmitting },
  } = useForm<RegisterValues>({ resolver: zodResolver(registerSchema) });

  // Check if email verification is required
  useState(() => {
    publicRequest<{ required: boolean }>("/api/portal/v1/auth/registration-email-verification")
      .then((d) => setVerificationRequired(d.required === true))
      .catch(() => {});
  });

  async function sendCode() {
    const email = getValues("email");
    if (!email) return;
    setSendingCode(true);
    try {
      await publicRequest("/api/portal/v1/auth/registration-email-verification", {
        method: "POST",
        body: JSON.stringify({ email }),
      });
      setVerificationSent(true);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "发送失败");
    } finally {
      setSendingCode(false);
    }
  }

  async function onSubmit(values: RegisterValues) {
    setError("");
    try {
      const data = await publicRequest<SessionResponse>(
        "/api/portal/v1/auth/register",
        { method: "POST", body: JSON.stringify(values) },
      );
      onSuccess(data.user, data.session.audience as "portal" | "admin", data.session.session_token);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "注册失败");
    }
  }

  return (
    <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
      {/* Mode tabs */}
      <div className="flex gap-1 rounded-lg bg-surface-2 p-1">
        <button type="button" onClick={onSwitchLogin} className="flex-1 rounded-md px-3 py-1.5 text-xs font-medium text-muted hover:text-foreground">
          登录
        </button>
        <button type="button" className="flex-1 rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground">
          注册
        </button>
      </div>

      <FieldGroup>
        <InputField
          icon={User}
          placeholder="显示名称 (可选)"
          autoComplete="name"
          error={errors.display_name?.message}
          {...register("display_name")}
        />
        <InputField
          icon={Mail}
          placeholder="邮箱"
          type="email"
          autoComplete="email"
          error={errors.email?.message}
          {...register("email")}
        />
        {verificationRequired && (
          <div className="flex gap-2">
            <div className="flex-1">
              <InputField
                icon={KeyRound}
                placeholder="邮箱验证码"
                inputMode="numeric"
                error={errors.verification_code?.message}
                {...register("verification_code")}
              />
            </div>
            <button
              type="button"
              onClick={sendCode}
              disabled={sendingCode}
              className={cn(
                "shrink-0 rounded-md border border-border px-3 text-xs font-medium transition-colors",
                verificationSent
                  ? "border-success/30 bg-success/5 text-success"
                  : "hover:bg-surface-2",
              )}
            >
              {sendingCode ? <Loader2 size={14} className="animate-spin" /> : verificationSent ? "已发送" : "发送"}
            </button>
          </div>
        )}
        <InputField
          icon={Lock}
          placeholder="密码 (至少 8 位)"
          type="password"
          autoComplete="new-password"
          error={errors.password?.message}
          {...register("password")}
        />
      </FieldGroup>

      {error && <ErrorMessage message={error} />}

      <SubmitButton loading={isSubmitting}>创建账号</SubmitButton>
    </form>
  );
}

/* ─── Reset Form ─── */

function ResetForm({ onBack }: { onBack: () => void }) {
  const [step, setStep] = useState<"request" | "confirm">("request");
  const [error, setError] = useState("");
  const [token, setToken] = useState("");
  const [success, setSuccess] = useState("");

  const fragmentToken = useMemo(
    () => new URLSearchParams(window.location.hash.replace(/^#/, "")).get("reset_token") ?? "",
    [],
  );

  // If URL has reset_token, jump to confirm
  useState(() => {
    if (fragmentToken) {
      setToken(fragmentToken);
      setStep("confirm");
    }
  });

  const requestForm = useForm<ResetRequestValues>({
    resolver: zodResolver(resetRequestSchema),
  });
  const confirmForm = useForm<ResetConfirmValues>({
    resolver: zodResolver(resetConfirmSchema),
    defaultValues: { token: fragmentToken },
  });

  async function onRequestSubmit(values: ResetRequestValues) {
    setError("");
    try {
      const data = await publicRequest<{ reset_token?: string }>(
        "/api/portal/v1/auth/password-reset/request",
        { method: "POST", body: JSON.stringify(values) },
      );
      if (data.reset_token) {
        setToken(data.reset_token);
        confirmForm.setValue("token", data.reset_token);
      }
      setStep("confirm");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "请求失败");
    }
  }

  async function onConfirmSubmit(values: ResetConfirmValues) {
    setError("");
    try {
      await publicRequest("/api/portal/v1/auth/password-reset/confirm", {
        method: "POST",
        body: JSON.stringify({ token: values.token, password: values.password }),
      });
      setSuccess("密码已重置，请返回登录。");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "重置失败");
    }
  }

  return (
    <div className="space-y-4">
      <button
        type="button"
        onClick={onBack}
        className="flex items-center gap-1 text-xs text-muted hover:text-foreground transition-colors"
      >
        <ChevronLeft size={14} /> 返回登录
      </button>

      <h2 className="text-sm font-semibold">重置密码</h2>

      {step === "request" ? (
        <form onSubmit={requestForm.handleSubmit(onRequestSubmit)} className="space-y-4">
          <InputField
            icon={Mail}
            placeholder="注册邮箱"
            type="email"
            error={requestForm.formState.errors.email?.message}
            {...requestForm.register("email")}
          />
          {error && <ErrorMessage message={error} />}
          <SubmitButton loading={requestForm.formState.isSubmitting}>
            发送重置请求
          </SubmitButton>
        </form>
      ) : (
        <form onSubmit={confirmForm.handleSubmit(onConfirmSubmit)} className="space-y-4">
          <FieldGroup>
            <InputField
              icon={KeyRound}
              placeholder="重置令牌"
              error={confirmForm.formState.errors.token?.message}
              {...confirmForm.register("token")}
            />
            <InputField
              icon={Lock}
              placeholder="新密码 (至少 8 位)"
              type="password"
              autoComplete="new-password"
              error={confirmForm.formState.errors.password?.message}
              {...confirmForm.register("password")}
            />
          </FieldGroup>
          {error && <ErrorMessage message={error} />}
          {success && (
            <div className="rounded-lg bg-success/5 border border-success/20 px-3 py-2 text-xs text-success">
              {success}
            </div>
          )}
          <SubmitButton loading={confirmForm.formState.isSubmitting}>
            确认重置
          </SubmitButton>
        </form>
      )}
    </div>
  );
}

/* ─── Shared Primitives ─── */

function FadePanel({ children }: { children: React.ReactNode }) {
  return (
    <motion.div
      initial={{ opacity: 0, x: 8 }}
      animate={{ opacity: 1, x: 0 }}
      exit={{ opacity: 0, x: -8 }}
      transition={{ duration: 0.15 }}
    >
      {children}
    </motion.div>
  );
}

function FieldGroup({ children }: { children: React.ReactNode }) {
  return <div className="space-y-3">{children}</div>;
}

import { forwardRef, type InputHTMLAttributes } from "react";
import type { LucideIcon } from "lucide-react";

interface InputFieldProps extends InputHTMLAttributes<HTMLInputElement> {
  icon?: LucideIcon;
  error?: string;
}

const InputField = forwardRef<HTMLInputElement, InputFieldProps>(
  ({ icon: Icon, error, className, ...props }, ref) => (
    <div>
      <div className="relative">
        {Icon && (
          <Icon
            size={15}
            className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-muted-2"
          />
        )}
        <input
          ref={ref}
          className={cn(
            "w-full rounded-lg border bg-surface py-2.5 text-sm outline-none transition-colors",
            "focus:border-bronze-soft focus:ring-2 focus:ring-ring",
            Icon ? "pl-9 pr-3" : "px-3",
            error ? "border-destructive/50" : "border-input",
            className,
          )}
          {...props}
        />
      </div>
      {error && (
        <p className="mt-1 text-xs text-destructive">{error}</p>
      )}
    </div>
  ),
);
InputField.displayName = "InputField";

function ErrorMessage({ message }: { message: string }) {
  return (
    <div className="rounded-lg bg-destructive/5 border border-destructive/20 px-3 py-2 text-xs text-destructive">
      {message}
    </div>
  );
}

function SubmitButton({
  loading,
  children,
}: {
  loading: boolean;
  children: React.ReactNode;
}) {
  return (
    <button
      type="submit"
      disabled={loading}
      className={cn(
        "flex w-full items-center justify-center gap-2 rounded-lg bg-primary py-2.5 text-sm font-medium text-primary-foreground transition-opacity",
        loading && "opacity-70",
      )}
    >
      {loading ? (
        <Loader2 size={16} className="animate-spin" />
      ) : (
        <>
          {children}
          <ArrowRight size={14} />
        </>
      )}
    </button>
  );
}
